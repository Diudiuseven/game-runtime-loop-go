package loop

// scheduler runs registered systems in a stable registration order and
// isolates failures so a broken system never leaks half-updated state to
// later systems.
type scheduler struct {
	systems []System
	policy  ErrorPolicy
}

func newScheduler(policy ErrorPolicy) *scheduler {
	return &scheduler{policy: policy}
}

// register appends a system. Order of registration is the order of
// execution and nothing else influences it.
func (s *scheduler) register(sys System) {
	s.systems = append(s.systems, sys)
}

// find returns the index of the system with the given name, or -1.
func (s *scheduler) find(name string) int {
	for i, sys := range s.systems {
		if sys.Name() == name {
			return i
		}
	}
	return -1
}

// swap replaces the system with the same name, keeping its slot so the
// execution order is preserved. Returns false if no such system exists.
func (s *scheduler) swap(sys System) bool {
	i := s.find(sys.Name())
	if i < 0 {
		return false
	}
	s.systems[i] = sys
	return true
}

// runStep executes every system once for the given step. On failure the
// state is rolled back to what it was before the failing system ran, so
// subsequent systems (or the next step) never observe partial updates.
func (s *scheduler) runStep(ctx StepContext, st State, errs *[]SystemError) {
	for _, sys := range s.systems {
		before := st.Clone()
		err := safeUpdate(sys, ctx, st)
		if err == nil {
			continue
		}
		st.CopyFrom(before)
		*errs = append(*errs, *err)
		if s.policy == PolicyAbortStep {
			return
		}
	}
}

// safeUpdate calls sys.Update and converts panics into SystemError values.
func safeUpdate(sys System, ctx StepContext, st State) (err *SystemError) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(error)
			if !ok {
				e = &panicError{value: r}
			}
			err = &SystemError{Tick: ctx.Tick, System: sys.Name(), Err: e, Panic: true}
		}
	}()
	if uerr := sys.Update(ctx, st); uerr != nil {
		return &SystemError{Tick: ctx.Tick, System: sys.Name(), Err: uerr}
	}
	return nil
}

type panicError struct{ value any }

func (p *panicError) Error() string {
	return "panic: " + toString(p.value)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "non-string panic value"
}
