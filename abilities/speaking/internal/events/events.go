// Package events writes speech's session events to its Redis stream in the
// CloudEvents envelope of contracts/events.md.
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

const (
	Stream = "speech_events"
	// Source is the organ that sends (persona-centric); the types are the MRCPv2
	// resource's (RFC 6787: speechsynth).
	Source = "/abilities/speaking"

	SpeakComplete = "speechsynth.speak-complete"
	SessionClosed = "speechsynth.session-closed"
)

// Event is one thing that happened to a session. Subject is the session_id.
type Event struct {
	Type        string
	Subject     string
	Data        map[string]any
	Traceparent string
	Time        time.Time
}

// Publisher is where events go. A failure to publish is the caller's to log;
// the session goes on either way.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Fields is the stream entry for e: each CloudEvents attribute a ce_ field,
// the payload in data.
func Fields(e Event) (map[string]any, error) {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return nil, fmt.Errorf("events: marshal %s: %w", e.Type, err)
	}
	f := map[string]any{
		"ce_specversion":     "1.0",
		"ce_id":              newID(),
		"ce_source":          Source,
		"ce_type":            e.Type,
		"ce_subject":         e.Subject,
		"ce_time":            e.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
		"ce_datacontenttype": "application/json",
		"data":               string(data),
	}
	if e.Traceparent != "" {
		f["ce_traceparent"] = e.Traceparent
	}
	return f, nil
}

// Redis publishes to the speech_events stream.
type Redis struct {
	Client *redis.Client
}

func (r Redis) Publish(ctx context.Context, e Event) error {
	f, err := Fields(e)
	if err != nil {
		return err
	}
	return r.Client.XAdd(ctx, sharedredis.XAddArgs(Stream, f)).Err()
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
