package chat

import (
	"sync"
	"time"
)

// limiter holds the state for the engine's two rate limits: how fast one chat
// may send the agent work, and how often a stranger is answered. Neither is
// there for fairness between users, since only the owner's chats are served. They
// stop a runaway client, a loop between two bots, or a stranger flooding the
// bot, from costing model calls or from getting the bot banned by the chat service.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[int64]*bucket
	warned  map[int64]time.Time // when a chat was last told to slow down
	known   map[int64]time.Time // when a stranger was last answered
	global  []time.Time         // times of recent answers to strangers
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	strangerEvery  = 10 * time.Minute // a stranger is told "not authorized" this often at most
	strangerWindow = time.Minute
	strangerMax    = 6 // answers to strangers in all, per window
	warnEvery      = 30 * time.Second
	maxTracked     = 2000
)

func (l *limiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// allowMessage takes one message's worth from a chat's allowance of perMinute (with a
// burst of the same size). warn says the chat should be told it is going too fast.
func (l *limiter) allowMessage(chat int64, perMinute int) (ok, warn bool) {
	if perMinute <= 0 {
		return true, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if l.buckets == nil {
		l.buckets, l.warned = map[int64]*bucket{}, map[int64]time.Time{}
	}
	b := l.buckets[chat]
	if b == nil {
		b = &bucket{tokens: float64(perMinute), last: now}
		l.buckets[chat] = b
	}
	b.tokens = min(float64(perMinute), b.tokens+now.Sub(b.last).Minutes()*float64(perMinute))
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, false
	}
	if now.Sub(l.warned[chat]) >= warnEvery {
		l.warned[chat] = now
		return false, true
	}
	return false, false
}

// allowStrangerReply says whether to answer someone who is not allowed in: once
// in a while per person, and only a few in all, however many people write.
func (l *limiter) allowStrangerReply(user int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if l.known == nil {
		l.known = map[int64]time.Time{}
	}
	if len(l.known) > maxTracked { // bounded: forget the oldest half-hour
		for u, t := range l.known {
			if now.Sub(t) > strangerEvery {
				delete(l.known, u)
			}
		}
		if len(l.known) > maxTracked {
			return false
		}
	}
	if t, seen := l.known[user]; seen && now.Sub(t) < strangerEvery {
		return false
	}
	recent := l.global[:0]
	for _, t := range l.global {
		if now.Sub(t) < strangerWindow {
			recent = append(recent, t)
		}
	}
	l.global = recent
	if len(l.global) >= strangerMax {
		return false
	}
	l.global = append(l.global, now)
	l.known[user] = now
	return true
}
