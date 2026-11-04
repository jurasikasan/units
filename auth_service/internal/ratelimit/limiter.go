package ratelimit

import (
	"context"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

type Limiter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
}

func NewLimiter(rdb *redis.Client, limit int, window time.Duration) *Limiter {
	return &Limiter{rdb: rdb, limit: limit, window: window}
}

func (l *Limiter) Allow(ctx context.Context, key string) bool {
	k := "ratelimit:" + key

	pipe := l.rdb.TxPipeline()
	incr := pipe.Incr(ctx, k)

	pipe.Expire(ctx, k, l.window)
	if _, err := pipe.Exec(ctx); err != nil {
		return true
	}

	return incr.Val() <= int64(l.limit)
}

func (l *Limiter) Reset(ctx context.Context, key string) {
	l.rdb.Del(ctx, "ratelimit:"+key)
}

func Key(parts ...string) string {
	return strings.Join(parts, "|")
}
