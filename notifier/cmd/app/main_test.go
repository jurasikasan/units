package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/jurasikasan/units/notifier/internal/ops"
	sharedOps "github.com/jurasikasan/units/shared/ops"

	"github.com/go-redis/redis/v8"
)

type fakeSender struct {
	sent []string
	err  error
}

func (f *fakeSender) Send(_ context.Context, text string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, text)
	return nil
}

func unreachableRedis() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Second})
}

func TestRunSendsExactlyOneStartupMessageBeforeReadingTheStream(t *testing.T) {
	sent := &fakeSender{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	run(ctx, unreachableRedis(), sent, ops.NewLimiter(time.Minute), time.Minute,
		sharedOps.SeverityError, "agent", "v1.2.3")

	if len(sent.sent) != 1 {
		t.Fatalf("start sent %d message(s), want exactly one: %q", len(sent.sent), sent.sent)
	}
	msg := sent.sent[0]
	if !strings.Contains(msg, "agent") {
		t.Errorf("startup message %q does not name the instance", msg)
	}
	if !strings.Contains(msg, "v1.2.3") {
		t.Errorf("startup message %q does not carry the version", msg)
	}
}

func TestRunStartupSendErrorIsLoggedAndDoesNotStopTheService(t *testing.T) {
	var logBuf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(old)

	sent := &fakeSender{err: errors.New("telegram down")}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	run(ctx, unreachableRedis(), sent, ops.NewLimiter(time.Minute), time.Minute,
		sharedOps.SeverityError, "agent", "v1.2.3")

	if len(sent.sent) != 0 {
		t.Fatalf("the refused message must not be retried or queued, got %q", sent.sent)
	}
	out := logBuf.String()
	if !strings.Contains(out, "telegram down") {
		t.Errorf("startup send error was not logged; log output: %q", out)
	}

	if !strings.Contains(out, "waiting for stream") {
		t.Errorf("the service stopped before reaching the stream loop; log output: %q", out)
	}
}
