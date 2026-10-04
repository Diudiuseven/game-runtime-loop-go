// Package loop implements a deterministic fixed-timestep game runtime loop.
//
// The loop advances game logic in fixed steps. Logical state is a pure
// function of the sequence of steps and inputs: it never depends on the
// wall clock, render cadence, or per-frame cost.
package loop

import (
	"fmt"
	"time"
)

// Tick is the index of a fixed logic step. Logical time of tick N is
// always N * Step, independent of any real clock.
type Tick uint64

// StepContext is handed to systems on every fixed step.
type StepContext struct {
	Tick  Tick          // index of the current step
	Delta time.Duration // fixed step duration, always Config.Step
	Time  time.Duration // logical time at start of step: Tick * Delta
}

// State is the deterministic simulation state. It must be deep-clonable
// so the scheduler can roll back half-updated state and the loop can take
// snapshots.
type State interface {
	// Clone returns a deep copy that shares no mutable memory with the receiver.
	Clone() State
	// CopyFrom restores the receiver to an exact copy of src.
	CopyFrom(src State)
}

// System is an ordered unit of logic executed once per fixed step.
type System interface {
	Name() string
	Update(ctx StepContext, s State) error
}

// Validatable is an optional interface. A system that implements it is
// validated before a hot swap is committed; a failed validation keeps the
// previous version in place.
type Validatable interface {
	Validate() error
}

// ErrorPolicy decides how the scheduler reacts when a system fails.
type ErrorPolicy int

const (
	// PolicyAbortStep rolls the state back to before the failing system and
	// skips the remaining systems for this step.
	PolicyAbortStep ErrorPolicy = iota
	// PolicyIsolate rolls the state back to before the failing system and
	// continues with the next system.
	PolicyIsolate
)

// OverflowPolicy decides what happens when accumulated time exceeds what
// MaxCatchUp steps can consume in one Advance call.
type OverflowPolicy int

const (
	// OverflowDrop discards the excess accumulated time. The simulation
	// skips forward; the dropped amount is reported.
	OverflowDrop OverflowPolicy = iota
	// OverflowDefer keeps a bounded backlog (at most MaxCatchUp steps worth
	// of time) so the simulation lags real time with a bounded, reported
	// delay instead of skipping. Anything beyond the bound is dropped.
	OverflowDefer
)

// Config tunes the loop. All fields are fixed at construction.
type Config struct {
	Step        time.Duration  // fixed logic step, must be > 0
	MaxCatchUp  int            // max steps executed per Advance call, must be > 0
	Overflow    OverflowPolicy // what to do with time beyond MaxCatchUp
	ErrorPolicy ErrorPolicy    // how to react to system errors/panics
}

func (c Config) validate() error {
	if c.Step <= 0 {
		return fmt.Errorf("loop: Step must be > 0, got %v", c.Step)
	}
	if c.MaxCatchUp <= 0 {
		return fmt.Errorf("loop: MaxCatchUp must be > 0, got %d", c.MaxCatchUp)
	}
	return nil
}

// SystemError reports a failure of one system during one step.
type SystemError struct {
	Tick   Tick
	System string
	Err    error
	Panic  bool // true if the failure was a recovered panic
}

func (e SystemError) Error() string {
	kind := "error"
	if e.Panic {
		kind = "panic"
	}
	return fmt.Sprintf("loop: system %q %s at tick %d: %v", e.System, kind, e.Tick, e.Err)
}

// Result describes what one Advance call did. Overload outcomes are
// distinguishable: Dropped>0 means time was discarded, Backlog>0 means a
// bounded deferral is pending.
type Result struct {
	Steps   int           // fixed steps executed
	Dropped time.Duration // accumulated time discarded (overload)
	Backlog time.Duration // bounded deferred time remaining (OverflowDefer)
	Errors  []SystemError // system failures observed, in order
	Swaps   []SwapReport  // hot swaps committed at step boundaries
}

// SwapReport records the outcome of a hot-swap request.
type SwapReport struct {
	System  string
	Applied bool  // false => rolled back to previous version
	Err     error // validation/rollback reason when Applied is false
}

// Snapshot captures the full loop state so execution can resume later and
// produce a trajectory identical to an uninterrupted run.
type Snapshot struct {
	Tick        Tick
	Accumulated time.Duration
	Paused      bool
	TimeScale   float64
	State       State
}
