package hearing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"sync"
	"time"

	redis "github.com/go-redis/redis/v8"
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
// CloudEvents 1.0 binary-mode envelope (contracts/events.md at the repository root).
type RedisSink struct {
	client *redis.Client
	stream string
	source string
}

// NewRedisSink connects to Redis (REDIS_ADDR) and returns a sink for
// the hearing_events stream.
func NewRedisSink(ctx context.Context) *RedisSink {
	return &RedisSink{
		client: sharedredis.GetRedisClient(ctx),
		stream: EventStream,
		source: EventSource,
	}
}

// Emit implements Sink.
func (r *RedisSink) Emit(ctx context.Context, ev Event) {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		log.Printf("hearing: encode event %s: %v", ev.Type, err)
		return
	}
	values := map[string]any{
		"ce_specversion":     "1.0",
		"ce_id":              newUUID(),
		"ce_source":          r.source,
		"ce_type":            ev.Type,
		"ce_subject":         ev.Subject,
		"ce_time":            time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"ce_datacontenttype": "application/json",
		"data":               string(data),
	}
	// ce_traceparent is the trace of the open call that caused this event
	// (contracts/events.md); an open without one means no field at all, never a
	// made-up value.
	if ev.Traceparent != "" {
		values["ce_traceparent"] = ev.Traceparent
	}
	args := sharedredis.XAddArgs(r.stream, values)
	if _, err := r.client.XAdd(ctx, args).Result(); err != nil {
		log.Printf("hearing: XAdd %s: %v", r.stream, err)
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

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80
	hexs := hex.EncodeToString(b)
	return hexs[0:8] + "-" + hexs[8:12] + "-" + hexs[12:16] + "-" + hexs[16:20] + "-" + hexs[20:32]
}
