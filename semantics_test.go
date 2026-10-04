package gameloop

import (
	"errors"
	"reflect"
	"testing"
)

// 暂停期间逻辑时间不得前进；恢复后暂停期间的真实时间不计入。
func TestPauseResume(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{}, &clock)

	clock.Advance(3 * testStep)
	l.Pump()
	if l.Step() != 3 {
		t.Fatalf("step=%d, want 3", l.Step())
	}

	l.Pause()
	clock.Advance(100 * testStep) // 暂停期间的真实时间必须被丢弃
	if r := l.Pump(); r.StepsRun != 0 || l.Step() != 3 {
		t.Fatalf("paused pump advanced logic: %+v step=%d", r, l.Step())
	}

	l.Resume()
	clock.Advance(2 * testStep)
	if r := l.Pump(); r.StepsRun != 2 || l.Step() != 5 {
		t.Fatalf("resume misaligned: %+v step=%d", r, l.Step())
	}
}

// 缩放后的推进仍落在固定步长上；边界明确：不足一步的余量保留。
func TestTimeScale(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{}, &clock)

	l.SetScale(0.5)
	clock.Advance(testStep) // 0.5x 下只累积半个步长
	if r := l.Pump(); r.StepsRun != 0 {
		t.Fatalf("sub-step accumulation must not run: %+v", r)
	}
	clock.Advance(testStep) // 累计恰好一个步长
	if r := l.Pump(); r.StepsRun != 1 {
		t.Fatalf("scaled step boundary: %+v", r)
	}

	l.SetScale(2)
	clock.Advance(3 * testStep) // 2x 下等于 6 个步长
	if r := l.Pump(); r.StepsRun != 6 {
		t.Fatalf("2x scale: ran %d, want 6", r.StepsRun)
	}

	// 缩放切换边界：切换前已累积的余量不得丢失或被重复计算。
	l.SetScale(1)
	clock.Advance(testStep)
	if r := l.Pump(); r.StepsRun != 1 {
		t.Fatalf("after scale reset: ran %d, want 1", r.StepsRun)
	}
}

// 缩放为 0 时逻辑时间不前进，且与 Pause 状态可区分。
func TestZeroScale(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{}, &clock)
	l.SetScale(0)
	clock.Advance(50 * testStep)
	if r := l.Pump(); r.StepsRun != 0 {
		t.Fatalf("zero scale advanced: %+v", r)
	}
	if l.Stats().Paused {
		t.Fatalf("zero scale must not be reported as paused")
	}
}

// failSystem 在指定步出错或崩溃。
type failSystem struct {
	name     string
	failOn   map[uint64]bool
	panics   bool
	observed *[]uint64 // 记录该系统后续系统看到的值
}

func (f failSystem) Name() string { return f.name }
func (f failSystem) Update(ctx *StepContext) error {
	if f.failOn[ctx.Step] {
		if f.panics {
			panic("kaboom")
		}
		return errors.New("boom")
	}
	return nil
}

// observeSystem 记录本系统执行时看到的状态值。
type observeSystem struct {
	name string
	into *[]uint64
}

func (o observeSystem) Name() string { return o.name }
func (o observeSystem) Update(ctx *StepContext) error {
	*o.into = append(*o.into, ctx.State.(*testState).value)
	return nil
}

// 系统出错：当前步回滚，后续系统看不到半更新状态，Pump 以错误中止。
func TestErrorIsolationAbort(t *testing.T) {
	var clock ManualClock
	var observed []uint64
	l := NewLoop(Config{StepDuration: testStep, MaxStepsPerPump: 100}, &testState{}, &clock)
	l.RegisterSystem(addSystem{name: "add", delta: 7})
	l.RegisterSystem(failSystem{name: "fail", failOn: map[uint64]bool{3: true}})
	l.RegisterSystem(observeSystem{name: "obs", into: &observed})

	clock.Advance(10 * testStep)
	r := l.Pump()
	if r.Err == nil {
		t.Fatalf("expected abort error")
	}
	var se *StepError
	if !errors.As(r.Err, &se) || se.System != "fail" || se.Step != 3 || se.Panic {
		t.Fatalf("wrong error: %v", r.Err)
	}
	if r.StepsRun != 3 || l.Step() != 3 {
		t.Fatalf("committed steps=%d step=%d, want 3/3", r.StepsRun, l.Step())
	}
	// 观察者只在前 3 个已提交步执行过，且从未看到第 3 步的半更新值。
	if len(observed) != 3 {
		t.Fatalf("observer ran %d times, want 3 (no half-updated state)", len(observed))
	}
	// 状态保持在上一个已提交步。
	if got := len(stateOf(l).log); got != 0 {
		t.Fatalf("unexpected log entries: %d", got)
	}
}

// panic 与 error 走同一条隔离路径。
func TestPanicIsolation(t *testing.T) {
	var clock ManualClock
	l := NewLoop(Config{StepDuration: testStep, MaxStepsPerPump: 100}, &testState{}, &clock)
	l.RegisterSystem(failSystem{name: "panicker", failOn: map[uint64]bool{0: true}, panics: true})

	clock.Advance(testStep)
	r := l.Pump()
	var se *StepError
	if !errors.As(r.Err, &se) || !se.Panic {
		t.Fatalf("panic not converted to StepError: %v", r.Err)
	}
	if l.Step() != 0 {
		t.Fatalf("panicked step must not commit")
	}
}

// ErrorSkipStep：回滚后跳过出错步，循环继续，事件可观测。
func TestErrorSkipStep(t *testing.T) {
	var clock ManualClock
	var events []Event
	l := NewLoop(Config{
		StepDuration:    testStep,
		MaxStepsPerPump: 100,
		ErrorPolicy:     ErrorSkipStep,
		OnEvent:         func(e Event) { events = append(events, e) },
	}, &testState{}, &clock)
	l.RegisterSystem(addSystem{name: "add", delta: 7})
	l.RegisterSystem(failSystem{name: "fail", failOn: map[uint64]bool{2: true}})

	clock.Advance(5 * testStep)
	r := l.Pump()
	if r.Err != nil {
		t.Fatalf("skip policy must not abort: %v", r.Err)
	}
	if r.StepsRun != 4 || r.StepsSkipped != 1 || l.Step() != 5 {
		t.Fatalf("run=%d skipped=%d step=%d, want 4/1/5", r.StepsRun, r.StepsSkipped, l.Step())
	}
	found := false
	for _, e := range events {
		if e.Kind == EventStepSkipped && e.Step == 2 && e.SystemName == "fail" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing skip event: %v", events)
	}
}

// 热更新：在步边界生效，更新前后逻辑时间与状态对齐。
func TestHotSwapAlignment(t *testing.T) {
	var clock ManualClock
	l := NewLoop(Config{StepDuration: testStep, MaxStepsPerPump: 100}, &testState{}, &clock)
	l.RegisterSystem(addSystem{name: "add", delta: 1})

	clock.Advance(3 * testStep)
	l.Pump()
	before := stateOf(l).value
	beforeStep := l.Step()

	l.SwapSystem("add", addSystem{name: "add", delta: 10})
	// 交换请求本身不得改变逻辑时间与状态。
	if l.Step() != beforeStep || stateOf(l).value != before {
		t.Fatalf("swap request leaked into state")
	}

	clock.Advance(2 * testStep)
	l.Pump()
	// 手工重放验证对齐：前 3 步 delta=1，后 2 步 delta=10。
	ref := &testState{}
	for i := 0; i < 3; i++ {
		ref.mix(1)
	}
	for i := 0; i < 2; i++ {
		ref.mix(10)
	}
	if stateOf(l).value != ref.value {
		t.Fatalf("post-swap state misaligned: got %d want %d", stateOf(l).value, ref.value)
	}
	if l.Step() != 5 {
		t.Fatalf("step=%d, want 5", l.Step())
	}
}

// 热更新失败：回退到上一可用版本，循环不中断。
func TestHotSwapRollback(t *testing.T) {
	var clock ManualClock
	var events []Event
	l := NewLoop(Config{
		StepDuration:    testStep,
		MaxStepsPerPump: 100,
		ErrorPolicy:     ErrorAbort,
		HotSwapRollback: true,
		OnEvent:         func(e Event) { events = append(events, e) },
	}, &testState{}, &clock)
	l.RegisterSystem(addSystem{name: "add", delta: 1})

	clock.Advance(2 * testStep)
	l.Pump()

	// 新版本首次执行即失败。
	l.SwapSystem("add", failSystem{name: "add", failOn: map[uint64]bool{2: true, 3: true}})
	clock.Advance(3 * testStep)
	r := l.Pump()
	if r.Err != nil {
		t.Fatalf("rollback should keep loop alive: %v", r.Err)
	}
	if r.StepsRun != 3 || l.Step() != 5 {
		t.Fatalf("run=%d step=%d, want 3/5", r.StepsRun, l.Step())
	}
	// 全部 5 步都按旧版本 delta=1 演化。
	ref := &testState{}
	for i := 0; i < 5; i++ {
		ref.mix(1)
	}
	if stateOf(l).value != ref.value {
		t.Fatalf("rollback state mismatch")
	}
	found := false
	for _, e := range events {
		if e.Kind == EventSwapRolledBack {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing rollback event: %v", events)
	}
}

// 快照恢复：从同一点继续推进，轨迹与不中断时一致。
func TestSnapshotRestore(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{}, &clock)
	for s := 0; s < 20; s++ {
		l.SubmitInput(uint64(s), s*5)
	}

	clock.Advance(8 * testStep)
	l.Pump()
	snap := l.Snapshot()

	clock.Advance(12 * testStep)
	l.Pump()
	full := stateOf(l).log

	// 恢复快照，重放同样的时间序列。
	l.Restore(snap)
	if l.Step() != 8 {
		t.Fatalf("restored step=%d, want 8", l.Step())
	}
	clock.Advance(12 * testStep)
	l.Pump()
	if !reflect.DeepEqual(stateOf(l).log, full) {
		t.Fatalf("restored trajectory diverged")
	}

	// 同一快照可重复恢复（快照本身未被消耗）。
	l.Restore(snap)
	clock.Advance(12 * testStep)
	l.Pump()
	if !reflect.DeepEqual(stateOf(l).log, full) {
		t.Fatalf("second restore diverged")
	}
}

// 快照保留暂停与缩放状态。
func TestSnapshotPreservesTimeControls(t *testing.T) {
	var clock ManualClock
	l := newTestLoop(Config{}, &clock)
	l.SetScale(2)
	l.Pause()
	snap := l.Snapshot()
	l.Resume()
	l.SetScale(1)
	l.Restore(snap)
	st := l.Stats()
	if !st.Paused || st.Scale != 2 {
		t.Fatalf("controls not preserved: %+v", st)
	}
}
