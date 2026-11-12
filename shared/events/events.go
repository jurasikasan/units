// Package events writes a unit's events to its Redis stream in the CloudEvents
// 1.0 binary-mode envelope of contracts/events.md: each attribute a ce_ field of
// the stream entry, the payload as JSON in data.
//
// The envelope is all this package knows. Stream names, sources and event types
// are each unit's own and stay in the unit, where tools/check_unit.py finds them.
package events

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"

	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// Event is one thing that happened. Subject is the id of the thing it is about
// (a call, a session).
type Event struct {
	Type    string
	Subject string
	Data    map[string]any
	// Traceparent is the W3C trace context of whatever caused the event. Empty
	// means the entry has no ce_traceparent at all, never a made-up one.
	Traceparent string
	// Time is when it happened; zero means the moment the fields are built.
	Time time.Time
}

// Fields is the stream entry for e sent from source.
func Fields(source string, e Event) (map[string]any, error) {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return nil, fmt.Errorf("events: marshal %s: %w", e.Type, err)
	}
	at := e.Time
	if at.IsZero() {
		at = time.Now()
	}
	f := map[string]any{
		"ce_specversion":     "1.0",
		"ce_id":              NewID(),
		"ce_source":          source,
		"ce_type":            e.Type,
		"ce_subject":         e.Subject,
		"ce_time":            at.UTC().Format("2006-01-02T15:04:05.000Z"),
		"ce_datacontenttype": "application/json",
		"data":               string(data),
	}
	if e.Traceparent != "" {
		f["ce_traceparent"] = e.Traceparent
	}
	return f, nil
}

// Redis publishes events from Source to Stream.
type Redis struct {
	Client *redis.Client
	Stream string
	Source string
}

// Publish writes e as one entry on the stream.
func (r Redis) Publish(ctx context.Context, e Event) error {
	f, err := Fields(r.Source, e)
	if err != nil {
		return err
	}
	if err := r.Client.XAdd(ctx, sharedredis.XAddArgs(r.Stream, f)).Err(); err != nil {
		return fmt.Errorf("events: XAdd %s: %w", r.Stream, err)
	}
	return nil
}

// NewID is a random (version 4) UUID, the ce_id of an entry.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
