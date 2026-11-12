// Package events writes speech's session events to its Redis stream in the
// CloudEvents envelope of contracts/events.md; the envelope itself is
// shared/events.
package events

import (
	"context"

	"github.com/go-redis/redis/v8"

	sharedevents "github.com/jurasikasan/units/shared/events"
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
type Event = sharedevents.Event

// Publisher is where events go. A failure to publish is the caller's to log;
// the session goes on either way.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Redis is the publisher to the speech_events stream.
func Redis(client *redis.Client) sharedevents.Redis {
	return sharedevents.Redis{Client: client, Stream: Stream, Source: Source}
}
