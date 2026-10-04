package gameloop

import "time"

// StepContext 在一次逻辑步内传递给每个 System。
type StepContext struct {
	Step   uint64        // 当前逻辑步序号（从 0 开始）
	Delta  time.Duration // 固定步长，恒等于 Config.StepDuration
	State  State         // 可原地修改的逻辑状态
	Inputs []any         // 调度到本步的输入（按提交顺序）
}

// System 是一段有序调度的逻辑。Update 必须是确定性的：
// 相同的 (Step, Delta, State, Inputs) 必须产生相同的状态迁移。
type System interface {
	Name() string
	Update(ctx *StepContext) error
}

// SystemFunc 便于用函数实现 System。
type SystemFunc struct {
	SystemName string
	Fn         func(ctx *StepContext) error
}

func (s SystemFunc) Name() string { return s.SystemName }
func (s SystemFunc) Update(ctx *StepContext) error {
	return s.Fn(ctx)
}
