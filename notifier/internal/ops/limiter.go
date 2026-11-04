package ops

import (
	"sync"
	"time"
)

type Limiter struct {
	cooldown time.Duration

	now func() time.Time

	mu   sync.Mutex
	seen map[string]*entry
}

type entry struct {
	lastSent   time.Time
	suppressed int
}

func NewLimiter(cooldown time.Duration) *Limiter {
	return &Limiter{cooldown: cooldown, now: time.Now, seen: map[string]*entry{}}
}

func (l *Limiter) Allow(key string) (send bool, suppressed int) {
	if l.cooldown <= 0 {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.evictLocked(now)

	e, ok := l.seen[key]
	if !ok {
		l.seen[key] = &entry{lastSent: now}
		return true, 0
	}
	if now.Sub(e.lastSent) < l.cooldown {
		e.suppressed++
		return false, 0
	}

	n := e.suppressed
	e.lastSent, e.suppressed = now, 0
	return true, n
}

func (l *Limiter) evictLocked(now time.Time) {
	horizon := l.cooldown * 10
	for k, e := range l.seen {
		if now.Sub(e.lastSent) > horizon {
			delete(l.seen, k)
		}
	}
}
