package gameloop

import "time"

// Result 描述一次 Pump 的结果，过载与丢弃均可区分。
type Result struct {
	StepsRun     int   // 本次实际提交的逻辑步数
	StepsSkipped int   // 因系统错误被回滚跳过的步数
	StepsDropped int   // 因追帧/欠债上界被丢弃的步数
	Overloaded   bool  // Pump 结束时仍有未消化的欠债步数
	Err          error // 非 nil 表示 Pump 被错误中止
}

// Loop 是固定时间步的游戏运行时主循环。
// 不是并发安全的：所有方法必须由驱动方（单 goroutine）调用。
type Loop struct {
	cfg    Config
	clock  Clock
	state  State
	step   uint64
	acc    float64 // 已缩放、尚未凑满一步的累积时间（纳秒）
	paused bool
	scale  float64
	last   time.Duration

	systems []systemEntry
	inputs  map[uint64][]any
	swaps   []swapRequest
}

func NewLoop(cfg Config, initial State, clock Clock) *Loop {
	c := cfg.withDefaults()
	if clock == nil {
		clock = RealClock{}
	}
	return &Loop{
		cfg:    c,
		clock:  clock,
		state:  initial,
		scale:  1,
		last:   clock.Now(),
		inputs: map[uint64][]any{},
	}
}

// RegisterSystem 追加一个系统，执行顺序即注册顺序。
func (l *Loop) RegisterSystem(s System) {
	l.systems = append(l.systems, systemEntry{current: s})
}

// SubmitInput 把输入调度到指定逻辑步，保证确定性与真实时钟无关。
func (l *Loop) SubmitInput(atStep uint64, in any) {
	l.inputs[atStep] = append(l.inputs[atStep], in)
}

// Pump 由外部驱动节奏（渲染/采样）调用，按固定步长推进逻辑。
//
// 时间模型：真实时钟增量先乘以缩放系数进入累加器，
// 累加器只按整数个 StepDuration 兑换逻辑步，余量保留。
// 单次 Pump 兑换的步数受 MaxStepsPerPump 上界约束，
// 超出部分按 CatchUpPolicy 丢弃（报告）或留债（受限）。
func (l *Loop) Pump() Result {
	var res Result

	now := l.clock.Now()
	raw := now - l.last
	l.last = now
	if !l.paused && raw > 0 {
		l.acc += float64(raw) * l.scale
	}

	stepNanos := float64(l.cfg.StepDuration)
	owed := int(l.acc / stepNanos)
	run := owed
	if run > l.cfg.MaxStepsPerPump {
		run = l.cfg.MaxStepsPerPump
	}
	if excess := owed - run; excess > 0 {
		switch l.cfg.CatchUpPolicy {
		case CatchUpDrop:
			// 直接丢弃超出上界的累积时间，绝不静默。
			l.acc -= float64(excess) * stepNanos
			res.StepsDropped = excess
			l.emit(Event{Kind: EventStepsDropped, Step: l.step, Count: excess})
		case CatchUpDefer:
			// 留债到后续 Pump，但债务总量受限，超限部分丢弃。
			debtCap := float64(l.cfg.MaxAccumulatedSteps) * stepNanos
			if l.acc-float64(run)*stepNanos > debtCap {
				overflow := int((l.acc - float64(run)*stepNanos - debtCap) / stepNanos)
				l.acc -= float64(overflow) * stepNanos
				res.StepsDropped = overflow
				l.emit(Event{Kind: EventStepsDropped, Step: l.step, Count: overflow})
			}
		}
	}

	for i := 0; i < run; i++ {
		skipped, err := l.runStep()
		if err != nil {
			res.Err = err
			// 中止时未消耗的时间仍留在累加器，状态保持在上一个已提交的步。
			break
		}
		if skipped {
			res.StepsSkipped++
		} else {
			res.StepsRun++
		}
		l.acc -= stepNanos
		if l.acc < 0 {
			l.acc = 0
		}
	}

	if int(l.acc/stepNanos) > 0 {
		res.Overloaded = true
		l.emit(Event{Kind: EventOverload, Step: l.step, Count: int(l.acc / stepNanos)})
	}
	return res
}

// runStep 以事务方式执行一步：任一系统出错或崩溃，
// 状态回滚到步初，后续系统不会看到半更新状态。
// 返回 skipped=true 表示该步按策略被跳过（不计入提交）。
func (l *Loop) runStep() (skipped bool, err error) {
	l.applySwaps()

	checkpoint := l.state.Clone()
	ctx := &StepContext{
		Step:   l.step,
		Delta:  l.cfg.StepDuration,
		State:  l.state,
		Inputs: l.inputs[l.step],
	}
	for i := range l.systems {
		sysErr := l.callSystem(&l.systems[i], ctx)
		if sysErr == nil {
			continue
		}
		// 回滚到步初状态（克隆以保持 checkpoint 不被后续重试污染）。
		l.state = checkpoint.Clone()
		// 热更新回退：若出错系统处于试用期，换回上一可用版本并重试本步。
		if l.systems[i].probation && l.systems[i].previous != nil && l.cfg.HotSwapRollback {
			l.rollbackSwap(&l.systems[i])
			l.state = checkpoint.Clone()
			ctx.State = l.state
			retry := l.callSystem(&l.systems[i], ctx)
			if retry == nil {
				continue
			}
			sysErr = retry
			l.state = checkpoint.Clone()
		}
		switch l.cfg.ErrorPolicy {
		case ErrorSkipStep:
			l.step++
			delete(l.inputs, l.step-1)
			l.emit(Event{Kind: EventStepSkipped, Step: l.step - 1, SystemName: l.systems[i].current.Name(), Err: sysErr})
			return true, nil
		default: // ErrorAbort
			return false, sysErr
		}
	}
	// 本步全部系统成功，提交。
	for i := range l.systems {
		l.systems[i].probation = false
	}
	l.step++
	delete(l.inputs, l.step-1)
	l.emit(Event{Kind: EventStepExecuted, Step: l.step - 1})
	return false, nil
}

// callSystem 调用单个系统并把 panic 转换为错误。
func (l *Loop) callSystem(entry *systemEntry, ctx *StepContext) (err error) {
	name := entry.current.Name()
	defer func() {
		if r := recover(); r != nil {
			err = &StepError{Step: ctx.Step, System: name, Err: errFromRecover(r), Panic: true}
		}
	}()
	if e := entry.current.Update(ctx); e != nil {
		return &StepError{Step: ctx.Step, System: name, Err: e}
	}
	return nil
}

func errFromRecover(r any) error {
	if e, ok := r.(error); ok {
		return e
	}
	return &PanicError{Value: r}
}

// Pause 暂停逻辑时间，恢复前 Pump 不再推进。
func (l *Loop) Pause() {
	l.last = l.clock.Now()
	l.paused = true
}

// Resume 恢复逻辑时间；暂停期间的真实时间不计入累积。
func (l *Loop) Resume() {
	l.last = l.clock.Now()
	l.paused = false
}

// SetScale 设置时间缩放；推进仍落在固定步长上。
// scale 必须 >= 0；0 等价于暂停累积（但语义上仍可区分）。
func (l *Loop) SetScale(scale float64) {
	if scale < 0 {
		scale = 0
	}
	l.scale = scale
}

// State 返回当前逻辑状态（仅在 Pump 之间调用，中间态不可见）。
func (l *Loop) State() State { return l.state }

// Step 返回已提交的逻辑步数。
func (l *Loop) Step() uint64 { return l.step }

// Stats 是循环的运行时可观测数据。
type Stats struct {
	Step         uint64
	Paused       bool
	Scale        float64
	PendingNanos float64 // 累加器中不足一步的余量
	DebtSteps    int     // Defer 策略下尚未消化的欠债步数
}

func (l *Loop) Stats() Stats {
	return Stats{
		Step:         l.step,
		Paused:       l.paused,
		Scale:        l.scale,
		PendingNanos: l.acc,
		DebtSteps:    int(l.acc / float64(l.cfg.StepDuration)),
	}
}

func (l *Loop) emit(e Event) {
	if l.cfg.OnEvent != nil {
		l.cfg.OnEvent(e)
	}
}
