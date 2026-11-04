package redis

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

func TestWaitForRedisDeadline(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer func() { _ = rdb.Close() }()

	start := time.Now()
	err := waitForRedis(context.Background(), rdb, "127.0.0.1:1", 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error once the deadline elapsed, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waitForRedis took %s, want it to return shortly after the 200ms deadline", elapsed)
	}
}

func TestWaitForRedisContextCancelled(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer func() { _ = rdb.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := waitForRedis(ctx, rdb, "127.0.0.1:1", time.Minute)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error once ctx was cancelled, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waitForRedis took %s, want it to return shortly after cancellation", elapsed)
	}
}

func TestXAddArgsCaps(t *testing.T) {

	ops := XAddArgs(OpsEventsStream, map[string]any{"service": "s"})
	if ops.MaxLen != OpsEventsMaxLen || !ops.Approx {
		t.Fatalf("ops_events: got MaxLen=%d approx=%v", ops.MaxLen, ops.Approx)
	}

	eph := XAddArgs("llm_token_abc", map[string]any{"x": "y"})
	if eph.MaxLen != EphemeralStreamMaxLen {
		t.Fatalf("ephemeral: got MaxLen=%d", eph.MaxLen)
	}

	errEph := XAddArgs("llm_error_xyz", map[string]any{"x": "y"})
	if errEph.MaxLen != EphemeralStreamMaxLen || !errEph.Approx {
		t.Fatalf("llm_error_: got MaxLen=%d approx=%v", errEph.MaxLen, errEph.Approx)
	}

	durable := XAddArgs("user_input", map[string]any{"x": "y"})
	if durable.MaxLen != 0 {
		t.Fatalf("user_input must be uncapped, got MaxLen=%d", durable.MaxLen)
	}
}
