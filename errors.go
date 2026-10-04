package gameloop

import "fmt"

// PanicError 包装系统中恢复的非 error 类型 panic 值。
type PanicError struct{ Value any }

func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }

// StepError 包装某一步中某个系统产生的错误（含 panic 恢复）。
type StepError struct {
	Step   uint64
	System string
	Err    error
	Panic  bool
}

func (e *StepError) Error() string {
	if e.Panic {
		return fmt.Sprintf("gameloop: step %d: system %q panicked: %v", e.Step, e.System, e.Err)
	}
	return fmt.Sprintf("gameloop: step %d: system %q failed: %v", e.Step, e.System, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }
