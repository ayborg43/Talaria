package app

import (
	"fmt"
	"sync"
	"time"
)

// budget limits how many requests may go to the model in an hour, across every
// conversation, job, goal and watch this process runs. It is a guard against a loop,
// or something steering the agent, spending on the user's account without end; an
// ordinary day does not come near it.
type budget struct {
	mu    sync.Mutex
	max   int
	now   func() time.Time
	times []time.Time
}

// take records a request, or refuses it.
func (b *budget) take() error {
	if b == nil || b.max <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	keep := b.times[:0]
	for _, t := range b.times {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	b.times = keep
	if len(b.times) >= b.max {
		return fmt.Errorf("the limit of %d requests to the model in an hour is used up, so this stopped. It lifts as the hour passes; raise limits.model_calls_per_hour in the config if it is too low (0 removes it)", b.max)
	}
	b.times = append(b.times, now)
	return nil
}
