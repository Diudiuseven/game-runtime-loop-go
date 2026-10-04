package gameloop

// EventKind 标识可观测事件类型。
type EventKind int

const (
	EventStepExecuted   EventKind = iota // 一步成功提交
	EventStepSkipped                     // 一步因系统错误被回滚并跳过
	EventStepsDropped                    // 追帧/欠债上界导致时间被丢弃
	EventOverload                        // Pump 结束后仍有欠债未消化
	EventSystemSwapped                   // 系统实现被热更新
	EventSwapRolledBack                  // 新实现首次出错，回退到上一版本
)

// Event 是主循环对外上报的可观测事件。
type Event struct {
	Kind       EventKind
	Step       uint64
	SystemName string
	Count      int // 对 Dropped 事件表示丢弃步数
	Err        error
}
