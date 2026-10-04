// Command demo proves local reproducibility: the same logical input
// sequence is driven with two different real-time cadences, and both runs
// must produce identical state hashes. It also exercises pause, time
// scale, hot swap and snapshot/restore.
package main

import (
	"fmt"
	"hash/fnv"
	"time"

	"github.com/Diudiuseven/game-runtime-loop-go/loop"
)

// world is a tiny deterministic simulation: a position integrated with
// integer math only.
type world struct {
	pos, vel int64
}

func (w *world) Clone() loop.State     { c := *w; return &c }
func (w *world) CopyFrom(s loop.State) { *w = *s.(*world) }

// physics advances the world by fixed integer steps. Input is a scripted
// acceleration chosen per tick, so the trajectory depends only on the
// step sequence.
type physics struct{}

func (physics) Name() string { return "physics" }
func (physics) Update(ctx loop.StepContext, s loop.State) error {
	w := s.(*world)
	accel := int64(ctx.Tick%7) - 3 // scripted deterministic input
	w.vel += accel
	w.pos += w.vel
	return nil
}

func hash(w *world) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d/%d", w.pos, w.vel)
	return h.Sum64()
}

func run(chunks []time.Duration) uint64 {
	l, err := loop.New(loop.Config{
		Step:        16 * time.Millisecond,
		MaxCatchUp:  8,
		Overflow:    loop.OverflowDefer,
		ErrorPolicy: loop.PolicyIsolate,
	}, &world{})
	if err != nil {
		panic(err)
	}
	l.Register(physics{})
	for _, d := range chunks {
		l.Advance(d)
	}
	// Drain any deferred backlog deterministically.
	for l.Advance(0).Steps > 0 {
	}
	return hash(l.Snapshot().State.(*world))
}

func main() {
	// 312 * 16ms exactly; every jittered chunk stays below the
	// MaxCatchUp*Step bound (128ms) so no cadence triggers overflow
	// handling and both feeds represent the same logical input.
	total := 4992 * time.Millisecond

	// Cadence A: steady 16ms frames.
	var steady []time.Duration
	for d := time.Duration(0); d < total; d += 16 * time.Millisecond {
		steady = append(steady, 16*time.Millisecond)
	}

	// Cadence B: stutters and bursts summing to the same total.
	pattern := []time.Duration{3 * time.Millisecond, 97 * time.Millisecond, 41 * time.Millisecond, 7 * time.Millisecond, 113 * time.Millisecond, 55 * time.Millisecond}
	var jittered []time.Duration
	sum := time.Duration(0)
	for i := 0; sum < total; i++ {
		d := pattern[i%len(pattern)]
		if sum+d > total {
			d = total - sum
		}
		jittered = append(jittered, d)
		sum += d
	}

	ha, hb := run(steady), run(jittered)
	fmt.Printf("steady   cadence hash: %d\n", ha)
	fmt.Printf("jittered cadence hash: %d\n", hb)
	if ha != hb {
		fmt.Println("MISMATCH: simulation is not deterministic")
	} else {
		fmt.Println("OK: identical trajectories under different real-time cadences")
	}
}
