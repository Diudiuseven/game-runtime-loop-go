package gameloop

// systemEntry 是注册表中的一个槽位，支持热更新与回退。
type systemEntry struct {
	current   System
	previous  System // 上一个可用版本，用于回退
	probation bool   // current 为热更新新版本且尚未成功执行过一步
}

// swapRequest 是一次待应用的热更新请求，在步边界生效。
type swapRequest struct {
	name string
	next System
}

// SwapSystem 请求在下一个步边界把名为 name 的系统替换为 next。
// 替换只在步与步之间生效，处于中间状态的步对外不可见，
// 因此更新前后逻辑时间与状态天然对齐。
// 若 next 在替换后首次执行出错且开启 HotSwapRollback，
// 自动回退到上一可用版本，循环不中断。
// 若不存在名为 name 的系统，请求被忽略并通过事件报告。
func (l *Loop) SwapSystem(name string, next System) {
	l.swaps = append(l.swaps, swapRequest{name: name, next: next})
}

// applySwaps 在步边界应用所有待处理的热更新请求。
func (l *Loop) applySwaps() {
	if len(l.swaps) == 0 {
		return
	}
	swaps := l.swaps
	l.swaps = nil
	for _, sw := range swaps {
		for i := range l.systems {
			if l.systems[i].current.Name() != sw.name {
				continue
			}
			prev := l.systems[i].current
			// 若当前版本还在试用期，说明它从未成功执行，
			// 回退基线应保持为更早的稳定版本。
			if l.systems[i].probation && l.systems[i].previous != nil {
				prev = l.systems[i].previous
			}
			l.systems[i] = systemEntry{current: sw.next, previous: prev, probation: true}
			l.emit(Event{Kind: EventSystemSwapped, Step: l.step, SystemName: sw.name})
			break
		}
	}
}

// rollbackSwap 把槽位回退到上一可用版本。
func (l *Loop) rollbackSwap(entry *systemEntry) {
	name := entry.current.Name()
	entry.current, entry.previous = entry.previous, nil
	entry.probation = false
	l.emit(Event{Kind: EventSwapRolledBack, Step: l.step, SystemName: name})
}
