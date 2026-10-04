package gameloop_test

import (
	"fmt"
	"time"

	gameloop "github.com/Diudiuseven/game-runtime-loop-go"
)

// counter 是最小确定性状态。
type counter struct{ n uint64 }

func (c *counter) Clone() gameloop.State { return &counter{n: c.n} }

// Example 演示：主循环完全由手动时钟驱动，可在本地确定性复现。
func Example() {
	clock := &gameloop.ManualClock{}
	loop := gameloop.NewLoop(gameloop.Config{
		StepDuration:    time.Second / 60,
		MaxStepsPerPump: 8,
		CatchUpPolicy:   gameloop.CatchUpDrop,
	}, &counter{}, clock)

	loop.RegisterSystem(gameloop.SystemFunc{
		SystemName: "tick",
		Fn: func(ctx *gameloop.StepContext) error {
			ctx.State.(*counter).n++
			return nil
		},
	})

	// 模拟外部驱动：渲染一帧耗时 1/60s。
	clock.Advance(time.Second / 60)
	loop.Pump()
	// 模拟一次卡顿：300ms 后才再次驱动，触发追帧（受上界约束）。
	clock.Advance(300 * time.Millisecond)
	res := loop.Pump()

	fmt.Println("step:", loop.Step())
	fmt.Println("ran:", res.StepsRun, "dropped:", res.StepsDropped)
	// Output:
	// step: 9
	// ran: 8 dropped: 10
}
