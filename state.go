package gameloop

// State 是游戏逻辑状态的最小抽象。
// 实现方必须保证 Clone 返回深拷贝：
// 修改副本不得影响原状态，反之亦然。
// 确定性要求：State 的演化只能由推进的时间步与输入决定。
type State interface {
	Clone() State
}
