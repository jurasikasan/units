package redis

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

const EphemeralStreamMaxLen = 2000

const OpsEventsMaxLen = 20000

var ephemeralStreamPrefixes = []string{"llm_token_", "llm_full_reply_", "llm_error_"}

func IsEphemeralStream(stream string) bool {
	for _, p := range ephemeralStreamPrefixes {
		if strings.HasPrefix(stream, p) {
			return true
		}
	}
	return false
}

var perishableStreams = map[string]bool{"llm_cancel": true, "board_events": true}

const OpsEventsStream = "ops_events"

func XAddArgs(stream string, values map[string]any) *redis.XAddArgs {
	args := &redis.XAddArgs{Stream: stream, Values: values}
	switch {
	case stream == OpsEventsStream:
		args.MaxLen = OpsEventsMaxLen
		args.Approx = true
	case IsEphemeralStream(stream) || perishableStreams[stream]:
		args.MaxLen = EphemeralStreamMaxLen
		args.Approx = true
	}
	return args
}

func connectTimeout() time.Duration {
	if v := os.Getenv("REDIS_CONNECT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 60 * time.Second
}

func waitForRedis(ctx context.Context, rdb *redis.Client, addr string, timeout time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)
	var lastErr error

	for {
		_, err := rdb.Ping(ctx).Result()
		if err == nil {
			return nil
		}
		lastErr = err
		log.Printf("Waiting for Redis at %s (retrying in 2s): %v", addr, err)

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("giving up on Redis at %s after %s: %w", addr, time.Since(start), lastErr)
		}
		wait := 2 * time.Second
		if remaining < wait {
			wait = remaining
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled waiting for Redis at %s after %s: %w", addr, time.Since(start), ctx.Err())
		case <-time.After(wait):
		}
	}
}

func GetRedisClient(ctx context.Context) *redis.Client {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	var rdb = redis.NewClient(&redis.Options{
		Addr: redisAddr,
		DB:   0,
	})

	if err := waitForRedis(ctx, rdb, redisAddr, connectTimeout()); err != nil {
		log.Fatalf("%v", err)
	}
	log.Printf("Connected to Redis at %s", redisAddr)

	return rdb
}
