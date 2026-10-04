package gameloop

import "time"

// CatchUpPolicy 决定单次 Pump 欠债步数超过上界时的行为。
type CatchUpPolicy int

const (
	// CatchUpDrop 丢弃超出上界的累积时间，并在结果中报告丢弃步数。
	CatchUpDrop CatchUpPolicy = iota
	// CatchUpDefer 保留欠债到后续 Pump（降级为逐帧消化），
	// 但欠债总量受 MaxAccumulatedSteps 约束，超出部分仍丢弃并报告。
	CatchUpDefer
)

// ErrorPolicy 决定某个 System 出错（含 panic）时的隔离策略。
type ErrorPolicy int

const (
	// ErrorAbort 回滚当前步并中止本次 Pump，错误通过 Result.Err 上报。
	ErrorAbort ErrorPolicy = iota
	// ErrorSkipStep 回滚当前步后跳过该步（视为未发生），继续后续推进，
	// 跳过事件通过 OnEvent 上报。
	ErrorSkipStep
)

// Config 是主循环的全部可调参数。
type Config struct {
	StepDuration        time.Duration // 固定逻辑步长，必须 > 0
	MaxStepsPerPump     int           // 单次 Pump 的步数上界（帧预算/追帧上界）
	CatchUpPolicy       CatchUpPolicy
	MaxAccumulatedSteps int // Defer 策略下欠债步数上限
	ErrorPolicy         ErrorPolicy
	HotSwapRollback     bool // 热更新后的系统首次出错时是否回退到上一可用版本
	OnEvent             func(Event)
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.StepDuration <= 0 {
		out.StepDuration = time.Second / 60
	}
	if out.MaxStepsPerPump <= 0 {
		out.MaxStepsPerPump = 5
	}
	if out.MaxAccumulatedSteps <= 0 {
		out.MaxAccumulatedSteps = out.MaxStepsPerPump * 4
	}
	out.HotSwapRollback = c.HotSwapRollback
	return out
}
