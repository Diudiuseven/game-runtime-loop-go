package loop

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// testState is a deterministic state: every system appends to log and
// adds to sum, so the full trajectory is observable.
type testState struct {
	sum int64
	log []string
}

func (s *testState) Clone() State {
	cp := &testState{sum: s.sum}
	cp.log = append(cp.log, s.log...)
	return cp
}

func (s *testState) CopyFrom(src State) {
	o := src.(*testState)
	s.sum = o.sum
	s.log = append(s.log[:0], o.log...)
}

// adder deterministically adds tick+weight to sum and logs itself.
type adder struct {
	name   string
	weight int64
}

func (a *adder) Name() string { return a.name }
func (a *adder) Update(ctx StepContext, s State) error {
	st := s.(*testState)
	st.sum += int64(ctx.Tick) + a.weight
	st.log = append(st.log, fmt.Sprintf("%s@%d", a.name, ctx.Tick))
	return nil
}

// failer mutates state and then fails (or panics), to verify rollback.
type failer struct {
	name  string
	panic bool
}

func (f *failer) Name() string { return f.name }
func (f *failer) Update(ctx StepContext, s State) error {
	st := s.(*testState)
	st.sum += 999999 // partial mutation that must be rolled back
	st.log = append(st.log, "partial")
	if f.panic {
		panic("boom")
	}
	return errors.New("injected failure")
}

func newTestLoop(t *testing.T, cfg Config, st *testState, systems ...System) *Loop {
	t.Helper()
	l, err := New(cfg, st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range systems {
		l.Register(s)
	}
	return l
}

var baseCfg = Config{Step: 10 * time.Millisecond, MaxCatchUp: 5, Overflow: OverflowDrop, ErrorPolicy: PolicyIsolate}

// TestDeterministicTrajectory feeds the same total time in different
// chunkings and requires identical state trajectories.
func TestDeterministicTrajectory(t *testing.T) {
	// Generous MaxCatchUp so no chunking triggers overflow handling.
	cfg := baseCfg
	cfg.MaxCatchUp = 1000
	run := func(chunks []time.Duration) *testState {
		st := &testState{}
		l := newTestLoop(t, cfg, st, &adder{name: "a", weight: 1}, &adder{name: "b", weight: 2})
		for _, d := range chunks {
			l.Advance(d)
		}
		return st
	}
	fine := make([]time.Duration, 0)
	for i := 0; i < 143; i++ {
		fine = append(fine, 7*time.Millisecond)
	}
	coarse := []time.Duration{500 * time.Millisecond, 3 * time.Millisecond, 498 * time.Millisecond}
	mixed := []time.Duration{23 * time.Millisecond, 977 * time.Millisecond}

	a, b, c := run(fine), run(coarse), run(mixed)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, c) {
		t.Fatalf("trajectories diverge:\nfine:   %v\ncoarse: %v\nmixed:  %v", a.log, b.log, c.log)
	}
	if len(a.log) != 2*100 { // 1001ms / 10ms = 100 steps, 2 systems each
		t.Fatalf("expected 200 log entries, got %d", len(a.log))
	}
}

// TestCatchUpBound verifies a long stall is bounded by MaxCatchUp and the
// excess is reported as dropped, not silently lost.
func TestCatchUpBound(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &adder{name: "a"})
	res := l.Advance(10 * time.Second) // huge stall
	if res.Steps != baseCfg.MaxCatchUp {
		t.Fatalf("steps = %d, want %d", res.Steps, baseCfg.MaxCatchUp)
	}
	wantDropped := 10*time.Second - time.Duration(baseCfg.MaxCatchUp)*baseCfg.Step
	if res.Dropped != wantDropped {
		t.Fatalf("dropped = %v, want %v", res.Dropped, wantDropped)
	}
	if l.Tick() != Tick(baseCfg.MaxCatchUp) {
		t.Fatalf("tick = %d", l.Tick())
	}
}

// TestOverflowDefer verifies the degrade mode: bounded backlog, no skip.
func TestOverflowDefer(t *testing.T) {
	cfg := baseCfg
	cfg.Overflow = OverflowDefer
	st := &testState{}
	l := newTestLoop(t, cfg, st, &adder{name: "a"})

	res := l.Advance(10 * time.Second)
	if res.Steps != cfg.MaxCatchUp {
		t.Fatalf("steps = %d", res.Steps)
	}
	if res.Backlog != time.Duration(cfg.MaxCatchUp)*cfg.Step {
		t.Fatalf("backlog = %v, want bounded %v", res.Backlog, time.Duration(cfg.MaxCatchUp)*cfg.Step)
	}
	if res.Dropped == 0 {
		t.Fatal("expected dropped time beyond the backlog bound")
	}
	// The backlog drains on subsequent advances instead of being skipped.
	res2 := l.Advance(0)
	if res2.Steps != cfg.MaxCatchUp {
		t.Fatalf("second advance steps = %d, want %d", res2.Steps, cfg.MaxCatchUp)
	}
	if l.Tick() != Tick(2*cfg.MaxCatchUp) {
		t.Fatalf("tick = %d", l.Tick())
	}
}

// TestPause verifies logical time freezes while paused and resume does
// not cause a catch-up burst.
func TestPause(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &adder{name: "a"})
	l.Advance(30 * time.Millisecond)
	if l.Tick() != 3 {
		t.Fatalf("tick = %d, want 3", l.Tick())
	}
	l.SetPaused(true)
	res := l.Advance(5 * time.Second) // long real pause
	if res.Steps != 0 || l.Tick() != 3 {
		t.Fatalf("paused advance ran %d steps, tick %d", res.Steps, l.Tick())
	}
	l.SetPaused(false)
	res = l.Advance(20 * time.Millisecond)
	if res.Steps != 2 || l.Tick() != 5 {
		t.Fatalf("after resume steps=%d tick=%d, want 2/5 (no burst)", res.Steps, l.Tick())
	}
	if res.Dropped != 0 {
		t.Fatalf("paused time leaked into accumulator: dropped=%v", res.Dropped)
	}
}

// TestTimeScale verifies scaled time still lands on the fixed step
// lattice and only affects time elapsed after the change.
func TestTimeScale(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &adder{name: "a"})
	l.Advance(10 * time.Millisecond) // 1 step at scale 1
	if err := l.SetTimeScale(2.0); err != nil {
		t.Fatal(err)
	}
	l.Advance(25 * time.Millisecond) // scaled to 50ms -> 5 steps
	if l.Tick() != 6 {
		t.Fatalf("tick = %d, want 6", l.Tick())
	}
	if err := l.SetTimeScale(0.5); err != nil {
		t.Fatal(err)
	}
	l.Advance(30 * time.Millisecond) // scaled to 15ms -> 1 step, 5ms remainder
	if l.Tick() != 7 {
		t.Fatalf("tick = %d, want 7", l.Tick())
	}
	if err := l.SetTimeScale(-1); err == nil {
		t.Fatal("negative scale accepted")
	}
}

// TestErrorIsolation verifies a failing system is rolled back and, under
// PolicyIsolate, later systems still run on clean state.
func TestErrorIsolation(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st,
		&adder{name: "before", weight: 1},
		&failer{name: "bad"},
		&adder{name: "after", weight: 1},
	)
	res := l.Advance(10 * time.Millisecond)
	if len(res.Errors) != 1 || res.Errors[0].System != "bad" || res.Errors[0].Panic {
		t.Fatalf("errors = %+v", res.Errors)
	}
	// "before" and "after" ran; the partial mutation of "bad" is gone.
	if !reflect.DeepEqual(st.log, []string{"before@0", "after@0"}) {
		t.Fatalf("log = %v", st.log)
	}
	if st.sum != 2 {
		t.Fatalf("sum = %d, want 2 (rollback failed)", st.sum)
	}
}

// TestPanicIsolation verifies panics are recovered and reported as such.
func TestPanicIsolation(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &failer{name: "crasher", panic: true}, &adder{name: "after"})
	res := l.Advance(10 * time.Millisecond)
	if len(res.Errors) != 1 || !res.Errors[0].Panic {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if !reflect.DeepEqual(st.log, []string{"after@0"}) {
		t.Fatalf("log = %v", st.log)
	}
}

// TestAbortPolicy verifies PolicyAbortStep skips the rest of the step.
func TestAbortPolicy(t *testing.T) {
	cfg := baseCfg
	cfg.ErrorPolicy = PolicyAbortStep
	st := &testState{}
	l := newTestLoop(t, cfg, st, &failer{name: "bad"}, &adder{name: "after"})
	res := l.Advance(10 * time.Millisecond)
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if len(st.log) != 0 {
		t.Fatalf("log = %v, want empty (step aborted)", st.log)
	}
}

// validSwap is a Validatable adder whose validation can be forced to fail.
type validSwap struct {
	adder
	failValidate bool
}

func (v *validSwap) Validate() error {
	if v.failValidate {
		return errors.New("invalid implementation")
	}
	return nil
}

// TestHotSwap verifies swaps commit at step boundaries, are reported, and
// failed validations roll back to the previous version.
func TestHotSwap(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &adder{name: "a", weight: 1})

	// Rejected swap: previous version keeps running.
	bad := &validSwap{adder: adder{name: "a", weight: 100}, failValidate: true}
	if err := l.Swap(bad); err == nil {
		t.Fatal("invalid swap accepted")
	}
	l.Advance(10 * time.Millisecond)
	if st.sum != 1 { // old weight still in effect
		t.Fatalf("sum = %d, want 1 (rollback to previous version)", st.sum)
	}

	// Valid swap commits at the next step boundary.
	good := &validSwap{adder: adder{name: "a", weight: 10}}
	if err := l.Swap(good); err != nil {
		t.Fatal(err)
	}
	res := l.Advance(10 * time.Millisecond)
	if len(res.Swaps) != 1 || !res.Swaps[0].Applied {
		t.Fatalf("swaps = %+v", res.Swaps)
	}
	if st.sum != 1+10+1 { // tick 1: weight 10
		t.Fatalf("sum = %d, want 12", st.sum)
	}

	// Swapping an unknown system is an error.
	if err := l.Swap(&adder{name: "ghost"}); err == nil {
		t.Fatal("swap of unknown system accepted")
	}
}

// TestSwapAlignment verifies a swap requested mid-batch only takes effect
// at a step boundary, never mid-step, and logical time stays aligned.
func TestSwapAlignment(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &adder{name: "a", weight: 1})
	l.Advance(20 * time.Millisecond) // ticks 0,1 with weight 1
	if err := l.Swap(&validSwap{adder: adder{name: "a", weight: 5}}); err != nil {
		t.Fatal(err)
	}
	l.Advance(20 * time.Millisecond) // ticks 2,3 with weight 5
	want := int64(0+1) + (1 + 1) + (2 + 5) + (3 + 5)
	if st.sum != want {
		t.Fatalf("sum = %d, want %d", st.sum, want)
	}
	if l.Tick() != 4 {
		t.Fatalf("tick = %d, want 4", l.Tick())
	}
}

// TestSnapshotRestore verifies resume-from-snapshot reproduces the
// uninterrupted trajectory exactly.
func TestSnapshotRestore(t *testing.T) {
	cfg := baseCfg
	feed := func(l *Loop) {
		for _, d := range []time.Duration{17 * time.Millisecond, 33 * time.Millisecond, 9 * time.Millisecond, 41 * time.Millisecond} {
			l.Advance(d)
		}
	}

	// Uninterrupted reference run.
	refState := &testState{}
	ref := newTestLoop(t, cfg, refState, &adder{name: "a", weight: 1}, &adder{name: "b", weight: 2})
	feed(ref)
	feed(ref)

	// Run, snapshot mid-way, restore, continue: must match reference.
	st := &testState{}
	l := newTestLoop(t, cfg, st, &adder{name: "a", weight: 1}, &adder{name: "b", weight: 2})
	feed(l)
	snap := l.Snapshot()
	feed(l) // diverge
	l.Restore(snap)
	feed(l)

	if !reflect.DeepEqual(refState, st) {
		t.Fatalf("restored trajectory diverges:\nref: %v\ngot: %v", refState.log, st.log)
	}
	if l.Tick() != ref.Tick() {
		t.Fatalf("tick = %d, want %d", l.Tick(), ref.Tick())
	}
}

// TestBurstAndIdle verifies irregular external cadence: bursts are
// bounded and reported, idle periods do nothing.
func TestBurstAndIdle(t *testing.T) {
	st := &testState{}
	l := newTestLoop(t, baseCfg, st, &adder{name: "a"})

	if res := l.Advance(0); res.Steps != 0 { // idle
		t.Fatalf("idle advance ran %d steps", res.Steps)
	}
	res := l.Advance(time.Second) // burst
	if res.Steps != baseCfg.MaxCatchUp || res.Dropped == 0 {
		t.Fatalf("burst result = %+v", res)
	}
	// After the burst is absorbed, normal pacing resumes.
	res = l.Advance(10 * time.Millisecond)
	if res.Steps != 1 || res.Dropped != 0 {
		t.Fatalf("post-burst result = %+v", res)
	}
}

// TestConfigValidation rejects unusable configs.
func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{Step: 0, MaxCatchUp: 1}, &testState{}); err == nil {
		t.Fatal("zero step accepted")
	}
	if _, err := New(Config{Step: time.Millisecond, MaxCatchUp: 0}, &testState{}); err == nil {
		t.Fatal("zero MaxCatchUp accepted")
	}
	if _, err := New(baseCfg, nil); err == nil {
		t.Fatal("nil state accepted")
	}
}
