package gameloop

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// testState 是确定性的测试状态：value 按 xorshift 混合输入演化，
// log 记录每步提交后的值，用于比较状态轨迹。
type testState struct {
	value uint64
	log   []uint64
}

func (s *testState) Clone() State {
	cp := &testState{value: s.value}
	cp.log = append([]uint64(nil), s.log...)
	return cp
}

func (s *testState) mix(x uint64) {
	s.value ^= x + 0x9e3779b97f4a7c15
	s.value ^= s.value << 13
	s.value ^= s.value >> 7
	s.value ^= s.value << 17
}

// addSystem 每步把 delta 与所有 int 输入混入状态。
type addSystem struct {
	name  string
	delta uint64
}

func (a addSystem) Name() string { return a.name }
func (a addSystem) Update(ctx *StepContext) error {
	st := ctx.State.(*testState)
	st.mix(a.delta)
	for _, in := range ctx.Inputs {
		if v, ok := in.(int); ok {
			st.mix(uint64(v))
		}
	}
	return nil
}

// logSystem 在每步末尾记录状态值，形成轨迹。
type logSystem struct{}

func (logSystem) Name() string { return "log" }
func (logSystem) Update(ctx *StepContext) error {
	st := ctx.State.(*testState)
	st.log = append(st.log, st.value)
	return nil
}

const testStep = 10 * time.Millisecond

func newTestLoop(cfg Config, clock *ManualClock) *Loop {
	cfg.StepDuration = testStep
	if cfg.MaxStepsPerPump == 0 {
		cfg.MaxStepsPerPump = 100000 // 确定性测试默认不触发追帧上界
	}
	l := NewLoop(cfg, &testState{}, clock)
	l.RegisterSystem(addSystem{name: "add", delta: 7})
	l.RegisterSystem(logSystem{})
	return l
}

func stateOf(l *Loop) *testState { return l.State().(*testState) }

// 确定性：相同输入序列、不同 Pump 节奏，轨迹必须一致。
func TestDeterminismAcrossPacing(t *testing.T) {
	var clockA, clockB ManualClock
	a := newTestLoop(Config{}, &clockA)
	b := newTestLoop(Config{}, &clockB)

	const steps = 200
	for s := 0; s < steps; s++ {
		if s%3 == 0 {
			a.SubmitInput(uint64(s), s*11)
			b.SubmitInput(uint64(s), s*11)
		}
	}

	// A：不规则大块时间推进。
	for _, chunk := range []int{1, 7, 2, 50, 3, 90, 47} {
		clockA.Advance(time.Duration(chunk) * testStep)
		r := a.Pump()
		if r.Err != nil {
			t.Fatalf("A pump: %v", r.Err)
		}
	}
	// B：一次推完全部时间。
	clockB.Advance(steps * testStep)
	r := b.Pump()
	if r.Err != nil {
		t.Fatalf("B pump: %v", r.Err)
	}

	if a.Step() != steps || b.Step() != steps {
		t.Fatalf("steps: a=%d b=%d, want %d", a.Step(), b.Step(), steps)
	}
	if stateOf(a).value != stateOf(b).value {
		t.Fatalf("final value diverged: a=%d b=%d", stateOf(a).value, stateOf(b).value)
	}
	if !reflect.DeepEqual(stateOf(a).log, stateOf(b).log) {
		t.Fatalf("trajectory diverged")
	}
}

// 单次循环耗时（真实时钟抖动）不影响逻辑结果。
func TestDeterminismIndependentOfWallClock(t *testing.T) {
	var clockA, clockB ManualClock
	a := newTestLoop(Config{}, &clockA)
	b := newTestLoop(Config{}, &clockB)

	// A：每次恰好一个步长。
	for i := 0; i < 100; i++ {
		clockA.Advance(testStep)
		a.Pump()
	}
	// B：先卡顿 100 倍步长，再一次 Pump（上限内）。
	clockB.Advance(100 * testStep)
	b.Pump()

	if stateOf(a).value != stateOf(b).value || a.Step() != b.Step() {
		t.Fatalf("wall-clock pacing leaked into logic: a=(%d,%d) b=(%d,%d)",
			a.Step(), stateOf(a).value, b.Step(), stateOf(b).value)
	}
}

// 追帧上界：卡顿后单次 Pump 步数受限，超出部分可区分地丢弃。
func TestCatchUpCapDrop(t *testing.T) {
	var clock ManualClock
	var events []Event
	l := newTestLoop(Config{
		MaxStepsPerPump: 5,
		CatchUpPolicy:   CatchUpDrop,
		OnEvent:         func(e Event) { events = append(events, e) },
	}, &clock)

	clock.Advance(23 * testStep)
	r := l.Pump()
	if r.Err != nil {
		t.Fatalf("pump: %v", r.Err)
	}
	if r.StepsRun != 5 {
		t.Fatalf("StepsRun=%d, want 5", r.StepsRun)
	}
	if r.StepsDropped != 18 {
		t.Fatalf("StepsDropped=%d, want 18", r.StepsDropped)
	}
	if r.Overloaded {
		t.Fatalf("Drop policy must not report overload after discarding")
	}
	// 丢弃后累加器只剩不足一步的余量，后续 Pump 不得再补跑。
	clock.Advance(0)
	if r2 := l.Pump(); r2.StepsRun != 0 {
		t.Fatalf("dropped time was silently replayed: ran %d steps", r2.StepsRun)
	}
	foundDrop := false
	for _, e := range events {
		if e.Kind == EventStepsDropped && e.Count == 18 {
			foundDrop = true
		}
	}
	if !foundDrop {
		t.Fatalf("missing EventStepsDropped(18), got %v", events)
	}
}

// Defer 策略：欠债逐帧消化，行为确定且可观测。
func TestCatchUpDeferDrainsDebt(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{
		MaxStepsPerPump:     2,
		CatchUpPolicy:       CatchUpDefer,
		MaxAccumulatedSteps: 100,
	}, &clock)

	clock.Advance(7 * testStep)
	r := l.Pump()
	if r.StepsRun != 2 || !r.Overloaded {
		t.Fatalf("first pump: run=%d overloaded=%v, want 2/true", r.StepsRun, r.Overloaded)
	}
	// 空闲期（无新时间）逐步消化债务。
	total := r.StepsRun
	for i := 0; i < 3; i++ {
		r = l.Pump()
		total += r.StepsRun
	}
	if total != 7 {
		t.Fatalf("total steps=%d, want 7", total)
	}
	if r.Overloaded {
		t.Fatalf("debt should be drained")
	}
	if l.Step() != 7 {
		t.Fatalf("step=%d, want 7", l.Step())
	}
}

// Defer 策略下债务总量受限，超限部分可区分地丢弃。
func TestCatchUpDeferDebtCap(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{
		MaxStepsPerPump:     2,
		CatchUpPolicy:       CatchUpDefer,
		MaxAccumulatedSteps: 4,
	}, &clock)

	clock.Advance(20 * testStep)
	r := l.Pump()
	if r.StepsRun != 2 {
		t.Fatalf("StepsRun=%d, want 2", r.StepsRun)
	}
	// 20 欠债 - 2 执行 = 18，债务上限 4，丢弃 14。
	if r.StepsDropped != 14 {
		t.Fatalf("StepsDropped=%d, want 14", r.StepsDropped)
	}
	if got := l.Stats().DebtSteps; got != 4 {
		t.Fatalf("DebtSteps=%d, want 4", got)
	}
}

// 突发请求：时钟未前进时反复 Pump 是确定性的空操作。
func TestBurstPumpsWithoutTime(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{}, &clock)
	for i := 0; i < 100; i++ {
		r := l.Pump()
		if r.StepsRun != 0 || r.Overloaded || r.Err != nil {
			t.Fatalf("burst pump %d misbehaved: %+v", i, r)
		}
	}
	if l.Step() != 0 {
		t.Fatalf("step advanced without time")
	}
}

var errBoom = errors.New("boom")

// 占位，后续测试文件继续。
var _ = errBoom
