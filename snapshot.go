package gameloop

// Snapshot 捕获循环的完整逻辑进度，可恢复后得到一致轨迹。
type Snapshot struct {
	Step   uint64
	Acc    float64
	Paused bool
	Scale  float64
	State  State
	Inputs map[uint64][]any
}

// Snapshot 深拷贝当前进度。注意：不捕获系统实现版本，
// 恢复后使用的是恢复时 Loop 中已注册的系统实现。
func (l *Loop) Snapshot() Snapshot {
	return Snapshot{
		Step:   l.step,
		Acc:    l.acc,
		Paused: l.paused,
		Scale:  l.scale,
		State:  l.state.Clone(),
		Inputs: cloneInputs(l.inputs),
	}
}

// Restore 从快照恢复；恢复快照本身不被修改（内部再克隆），
// 因此同一快照可重复用于多次恢复。
// 恢复后时钟基线重置：下一次 Pump 从调用时刻重新计时。
func (l *Loop) Restore(s Snapshot) {
	l.step = s.Step
	l.acc = s.Acc
	l.paused = s.Paused
	l.scale = s.Scale
	l.state = s.State.Clone()
	l.inputs = cloneInputs(s.Inputs)
	l.last = l.clock.Now()
}

func cloneInputs(in map[uint64][]any) map[uint64][]any {
	out := make(map[uint64][]any, len(in))
	for k, v := range in {
		cp := make([]any, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
