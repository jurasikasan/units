package brain

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"

	"github.com/jurasikasan/units/shared/events"
)

// The paths in, on a real Redis when BRAIN_TEST_REDIS_ADDR names one
// (check.sh starts one in docker), skipped otherwise: a consumer group's
// resume is Redis's behaviour, which a fake would only restate.

func liveRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("BRAIN_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("BRAIN_TEST_REDIS_ADDR not set")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func publish(t *testing.T, c *redis.Client, stream, source, typ string, data map[string]any) {
	t.Helper()
	err := events.Redis{Client: c, Stream: stream, Source: source}.Publish(context.Background(), events.Event{Type: typ, Subject: "s-1", Data: data})
	if err != nil {
		t.Fatal(err)
	}
}

func next(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func TestLiveStreamsReadAndResume(t *testing.T) {
	c := liveRedis(t)
	suffix := uuid.NewString()[:8]
	hearing, sight := "hearing_events_"+suffix, "sight_events_"+suffix
	t.Cleanup(func() { c.Del(context.Background(), hearing, sight) })

	// What was on a stream before the brain's group existed is not perceived.
	publish(t, c, hearing, "/senses/hearing", "speechrecog.recognition-complete",
		map[string]any{"request_id": "old", "text": "before", "started_at": stamp(ms(0))})

	s := Streams{Client: c, Names: []string{hearing, sight}, Group: "brain", Consumer: "brain"}
	if err := s.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(context.Background()); err != nil {
		t.Fatalf("a second Ensure: %v", err)
	}
	counts := &Counts{}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	run := func() (chan Event, context.CancelFunc, chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		out, done := make(chan Event, 8), make(chan struct{})
		go func() { s.Run(ctx, out, counts, log); close(done) }()
		return out, cancel, done
	}

	out, stop, done := run()
	publish(t, c, sight, "/senses/sight", "sightrecog.recognition-complete",
		map[string]any{"request_id": "obs-1", "text": "a hall", "started_at": stamp(ms(0))})
	if ev := next(t, out); ev.Channel != "sight" || ev.Percept.Text != "a hall" {
		t.Fatalf("%+v", ev)
	}
	_ = c.XAdd(context.Background(), &redis.XAddArgs{Stream: hearing, Values: map[string]any{"junk": "1"}}).Err()
	publish(t, c, hearing, "/senses/hearing", "speechrecog.start-of-input",
		map[string]any{"request_id": "utt-1", "started_at": stamp(ms(0))})
	if ev := next(t, out); ev.Kind != Start || ev.Channel != "hearing" {
		t.Fatalf("%+v", ev)
	}
	stop()
	<-done

	// While the brain is down a sense goes on; on restart it resumes there.
	publish(t, c, hearing, "/senses/hearing", "speechrecog.recognition-complete",
		map[string]any{"request_id": "utt-1", "text": "while down", "started_at": stamp(ms(0))})
	out, stop, done = run()
	defer func() { stop(); <-done }()
	if ev := next(t, out); ev.Percept == nil || ev.Percept.Text != "while down" {
		t.Fatalf("%+v", ev)
	}
	select {
	case ev := <-out:
		t.Fatalf("read again: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
	if counts.Malformed.Load() != 1 {
		t.Fatalf("malformed %d", counts.Malformed.Load())
	}
	pending, err := c.XPending(context.Background(), hearing, "brain").Result()
	if err != nil || pending.Count != 0 {
		t.Fatalf("pending %+v %v", pending, err)
	}
}

// A brain that stopped between taking an entry and acknowledging it reads
// that entry again on restart.
func TestLiveStreamsReadTheUnacknowledgedFirst(t *testing.T) {
	c := liveRedis(t)
	ctx := context.Background()
	stream := "hearing_events_" + uuid.NewString()[:8]
	t.Cleanup(func() { c.Del(ctx, stream) })
	s := Streams{Client: c, Names: []string{stream}, Group: "brain", Consumer: "brain"}
	if err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	publish(t, c, stream, "/senses/hearing", "speechrecog.recognition-complete",
		map[string]any{"request_id": "utt-1", "text": "taken, not acked", "started_at": stamp(ms(0))})
	if err := c.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "brain", Consumer: "brain", Streams: []string{stream, ">"}, Block: -1}).Err(); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(ctx)
	out, done := make(chan Event, 8), make(chan struct{})
	go func() { s.Run(rctx, out, &Counts{}, slog.New(slog.NewJSONHandler(io.Discard, nil))); close(done) }()
	defer func() { cancel(); <-done }()
	if ev := next(t, out); ev.Percept == nil || ev.Percept.Text != "taken, not acked" {
		t.Fatalf("%+v", ev)
	}
	publish(t, c, stream, "/senses/hearing", "speechrecog.recognition-complete",
		map[string]any{"request_id": "utt-2", "text": "new", "started_at": stamp(ms(0))})
	if ev := next(t, out); ev.Percept == nil || ev.Percept.Text != "new" {
		t.Fatalf("%+v", ev)
	}
}
