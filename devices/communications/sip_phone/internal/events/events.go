// Package events writes phone's call events to its Redis stream in the
// CloudEvents envelope of contracts/events.md; the envelope itself is
// shared/events.
package events

import (
	"context"

	"github.com/go-redis/redis/v8"

	sharedevents "github.com/jurasikasan/units/shared/events"
)

const (
	Stream = "phone_events"
	Source = "/devices/communications/sip_phone"

	CallIncoming = "phone.call.incoming"
	CallRinging  = "phone.call.ringing"
	CallAnswered = "phone.call.answered"
	CallEnded    = "phone.call.ended"
)

// Event is one thing that happened to a call. Subject is the call_id.
type Event = sharedevents.Event

// Publisher is where events go. A failure to publish is the caller's to log;
// the call goes on either way.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Redis is the publisher to the phone_events stream.
func Redis(client *redis.Client) sharedevents.Redis {
	return sharedevents.Redis{Client: client, Stream: Stream, Source: Source}
}
