package sight

import (
	"context"
	"log"
	"sync"

	sharedevents "github.com/jurasikasan/units/shared/events"
	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// Event is one sight event addressed to a session.
type Event struct {
	Type    string
	Subject string
	Data    map[string]any
	// Traceparent is the W3C traceparent of the open call that made the
	// session (contracts/events.md). Empty means the event carries no ce_traceparent.
	Traceparent string
}

// Sink receives events.
type Sink interface {
	Emit(ctx context.Context, ev Event)
}

// EventStream is the Redis stream sight events go to.
const EventStream = "sight_events"

// EventSource is the CloudEvents source of every event: the sense that sends.
const EventSource = "/senses/sight"

// Event types (CONTRACT §3): hearing's MRCPv2 recognizer shape under a
// resource of sight's own, sightrecog (CONTRACT §7 says why not ONVIF's topics).
const (
	TypeStartOfInput        = "sightrecog.start-of-input"
	TypeRecognitionComplete = "sightrecog.recognition-complete"
	TypeSessionClosed       = "sightrecog.session-closed"
)

// Completion causes, MRCPv2's (RFC 6787 §9.4.11), the ones sight reports.
const (
	CauseSuccess         = "success"          // 000
	CauseNoMatch         = "no-match"         // 001: the model saw nothing to say
	CauseRecognizerError = "recognizer-error" // 006: the vision door failed
)

// RedisSink writes events to the sight_events Redis stream in the
// CloudEvents 1.0 binary-mode envelope (contracts/events.md, shared/events).
type RedisSink struct {
	pub sharedevents.Redis
}

// NewRedisSink connects to Redis (REDIS_ADDR) and returns a sink for the
// sight_events stream.
func NewRedisSink(ctx context.Context) *RedisSink {
	return &RedisSink{pub: sharedevents.Redis{
		Client: sharedredis.GetRedisClient(ctx),
		Stream: EventStream,
		Source: EventSource,
	}}
}

// Emit implements Sink. A failure is logged; the session goes on.
func (r *RedisSink) Emit(ctx context.Context, ev Event) {
	err := r.pub.Publish(ctx, sharedevents.Event{
		Type:        ev.Type,
		Subject:     ev.Subject,
		Data:        ev.Data,
		Traceparent: ev.Traceparent,
	})
	if err != nil {
		log.Printf("sight: event %s: %v", ev.Type, err)
	}
}

// InMemorySink records events in memory; the tests' sink.
type InMemorySink struct {
	mu     sync.Mutex
	events []Event
}

// Emit implements Sink.
func (m *InMemorySink) Emit(_ context.Context, ev Event) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
}

// Events is a copy of what was emitted so far.
func (m *InMemorySink) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}
