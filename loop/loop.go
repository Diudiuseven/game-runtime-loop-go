package loop

import (
	"fmt"
	"sync"
	"time"
)

// Loop is a deterministic fixed-timestep runtime loop. It is driven
// externally via Advance: the caller supplies how much real time has
// elapsed and the loop converts it into a bounded number of fixed steps.
//
// Logical state depends only on the sequence of executed steps and the
// inputs those steps consume; never on wall-clock time, render cadence,
// or the cost of a single Advance call.
type Loop struct {
	cfg   Config
	sched *scheduler

	mu      sync.Mutex
	state   State
	tick    Tick
	acc     time.Duration // scaled, not-yet-simulated time
	paused  bool
	scale   float64
	pending []System // hot swaps waiting for a step boundary
}

// New creates a loop. The state is used in place (not cloned).
func New(cfg Config, state State) (*Loop, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("loop: state must not be nil")
	}
	return &Loop{
		cfg:   cfg,
		sched: newScheduler(cfg.ErrorPolicy),
		state: state,
		scale: 1,
	}, nil
}

// Register appends a system to the schedule. Registration order is the
// execution order. Must not be called concurrently with Advance.
func (l *Loop) Register(sys System) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sched.register(sys)
}

// Swap requests a hot replacement of the system with the same name. The
// new implementation is validated immediately; if validation fails the
// request is rejected and the previous version keeps running. A valid
// replacement is committed at the next step boundary, so a half-executed
// step is never observable and logical time stays aligned across the swap.
func (l *Loop) Swap(sys System) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sched.find(sys.Name()) < 0 {
		return fmt.Errorf("loop: no system named %q registered", sys.Name())
	}
	if v, ok := sys.(Validatable); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("loop: swap of %q rejected: %w", sys.Name(), err)
		}
	}
	l.pending = append(l.pending, sys)
	return nil
}

// SetPaused halts logical time. While paused, Advance executes no steps
// and accumulates nothing: real time spent paused never turns into logic
// time, so resuming does not trigger a catch-up burst.
func (l *Loop) SetPaused(paused bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paused = paused
}

// SetTimeScale scales subsequently elapsed real time before it is
// accumulated. Steps themselves always have the fixed Config.Step size,
// so scaled advancement still lands exactly on the step lattice. The new
// scale applies only to time elapsed after this call.
func (l *Loop) SetTimeScale(scale float64) error {
	if scale < 0 {
		return fmt.Errorf("loop: time scale must be >= 0, got %v", scale)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scale = scale
	return nil
}

// Advance feeds elapsed real time into the loop and executes the fixed
// steps it covers, at most Config.MaxCatchUp. Excess time is handled per
// Config.Overflow and reported in the Result, never silently.
//
// Advance is safe to call at any cadence: bursts are bounded by
// MaxCatchUp, idle periods simply produce zero steps.
func (l *Loop) Advance(elapsed time.Duration) Result {
	l.mu.Lock()
	defer l.mu.Unlock()

	var res Result
	if l.paused {
		l.applySwaps(&res)
		return res
	}

	// Scale first, then accumulate in integer nanoseconds: deterministic
	// for a given input sequence on any machine. A zero or negative
	// elapsed still drains any deferred backlog below.
	if elapsed > 0 {
		l.acc += time.Duration(float64(elapsed) * l.scale)
	}

	stepNanos := l.cfg.Step
	steps := int(l.acc / stepNanos)
	if steps > l.cfg.MaxCatchUp {
		steps = l.cfg.MaxCatchUp
	}

	res.Steps = steps
	for i := 0; i < steps; i++ {
		l.applySwaps(&res)
		ctx := StepContext{
			Tick:  l.tick,
			Delta: l.cfg.Step,
			Time:  time.Duration(l.tick) * l.cfg.Step,
		}
		l.sched.runStep(ctx, l.state, &res.Errors)
		l.tick++
		l.acc -= stepNanos
	}

	// Overflow: time beyond what MaxCatchUp steps could consume.
	if l.overflowed() {
		excess := l.acc
		switch l.cfg.Overflow {
		case OverflowDrop:
			dropped := excess - (excess % stepNanos)
			l.acc -= dropped
			res.Dropped += dropped
		case OverflowDefer:
			cap_ := time.Duration(l.cfg.MaxCatchUp) * stepNanos
			if l.acc > cap_ {
				res.Dropped += l.acc - cap_
				l.acc = cap_
			}
			res.Backlog = l.acc
		}
	}
	return res
}

// overflowed reports whether unconsumed whole steps remain after an
// Advance batch, i.e. the caller is feeding time faster than MaxCatchUp
// can drain.
func (l *Loop) overflowed() bool {
	return l.acc >= l.cfg.Step
}

// applySwaps commits pending hot swaps. Must be called with l.mu held and
// only at a step boundary.
func (l *Loop) applySwaps(res *Result) {
	for _, sys := range l.pending {
		rep := SwapReport{System: sys.Name()}
		if l.sched.swap(sys) {
			rep.Applied = true
		} else {
			rep.Err = fmt.Errorf("system %q no longer registered", sys.Name())
		}
		res.Swaps = append(res.Swaps, rep)
	}
	l.pending = l.pending[:0]
}

// Tick returns the current logical tick.
func (l *Loop) Tick() Tick {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tick
}

// Snapshot captures the full loop state. Restoring it later and feeding
// the same subsequent inputs yields a trajectory identical to an
// uninterrupted run.
func (l *Loop) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Snapshot{
		Tick:        l.tick,
		Accumulated: l.acc,
		Paused:      l.paused,
		TimeScale:   l.scale,
		State:       l.state.Clone(),
	}
}

// Restore returns the loop to a previously taken snapshot. Pending hot
// swaps are discarded; the schedule itself is not part of the snapshot.
func (l *Loop) Restore(snap Snapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tick = snap.Tick
	l.acc = snap.Accumulated
	l.paused = snap.Paused
	l.scale = snap.TimeScale
	l.state.CopyFrom(snap.State)
	l.pending = l.pending[:0]
}
