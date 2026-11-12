package hearing

import (
	"context"
	"log"
	"sync"

	sharedevents "github.com/jurasikasan/units/shared/events"
	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// Event is one hearing event addressed to a session.
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

// EventStream is the Redis stream hearing events go to.
const EventStream = "hearing_events"

// EventSource is the CloudEvents source of every event: the organ that sends
// (persona-centric). The types are the MRCPv2 resource's (RFC 6787: speechrecog).
const EventSource = "/senses/hearing"

// Event types (C5 §3), MRCPv2's recognizer events.
const (
	TypeStartOfInput        = "speechrecog.start-of-input"
	TypeRecognitionComplete = "speechrecog.recognition-complete"
	TypeSessionClosed       = "speechrecog.session-closed"
)

// Completion causes of RECOGNITION-COMPLETE (RFC 6787 §9.4.11) hearing reports.
const (
	CauseSuccess         = "success"          // 000
	CauseNoMatch         = "no-match"         // 001: speech to the detector, words to nobody
	CauseRecognizerError = "recognizer-error" // 006: transcription failed
	CauseSuccessMaxtime  = "success-maxtime"  // 008: cut at HEARING_MAX_UTTERANCE
)

// RedisSink writes events to the hearing_events Redis stream in the
// CloudEvents 1.0 binary-mode envelope (contracts/events.md, shared/events).
type RedisSink struct {
	pub sharedevents.Redis
}

// NewRedisSink connects to Redis (REDIS_ADDR) and returns a sink for
// the hearing_events stream.
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
		log.Printf("hearing: event %s: %v", ev.Type, err)
	}
}

// InMemorySink records events in memory; it is the sink used by the
// tests and is safe for small in-process consumers.
type InMemorySink struct {
	mu     sync.Mutex
	Events []Event
}

// Emit implements Sink.
func (m *InMemorySink) Emit(_ context.Context, ev Event) {
	m.mu.Lock()
	m.Events = append(m.Events, ev)
	m.mu.Unlock()
}
