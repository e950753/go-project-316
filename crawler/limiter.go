package crawler

import (
	"context"
	"sync"
	"time"
)

// limiter равномерно раздаёт «слоты» запросов на весь процесс.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(delay time.Duration, rps float64) *limiter {
	interval := delay
	if rps > 0 {
		interval = time.Duration(float64(time.Second) / rps)
	}
	if interval <= 0 {
		return nil
	}
	return &limiter{interval: interval}
}

// wait блокируется до своего слота или до отмены контекста.
func (l *limiter) wait(ctx context.Context) error {
	if l == nil {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.interval)
	l.mu.Unlock()

	return sleep(ctx, at.Sub(now))
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
