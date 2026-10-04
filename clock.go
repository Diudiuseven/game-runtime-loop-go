package gameloop

import "time"

// Clock 抽象真实时钟，使主循环可以在测试中脱离真实时间复现。
type Clock interface {
	Now() time.Duration
}

// RealClock 使用单调时钟。
type RealClock struct{}

func (RealClock) Now() time.Duration { return time.Since(realClockBase) }

var realClockBase = time.Now()

// ManualClock 是测试用手动时钟，只有调用 Advance 才前进。
type ManualClock struct {
	now time.Duration
}

func (c *ManualClock) Now() time.Duration { return c.now }

func (c *ManualClock) Advance(d time.Duration) { c.now += d }
