// Package events announces threads' appends on its Redis stream in the
// CloudEvents envelope of contracts/events.md; the envelope itself is
// shared/events. An event only wakes a reader: the journal is the truth, read
// back with after={seq}.
package events

import (
	"context"

	"github.com/go-redis/redis/v8"

	sharedevents "github.com/jurasikasan/units/shared/events"
)

const (
	Stream = "threads_events"
	Source = "/threads"

	// EntryAppended: one entry was appended to the thread named in subject.
	EntryAppended = "threads.entry.appended"
)

// Event is one thing that happened to a thread. Subject is the thread id.
type Event = sharedevents.Event

// Publisher is where events go. A failure to publish is the caller's to log;
// the append stands either way.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Redis is the publisher to the threads_events stream.
func Redis(client *redis.Client) sharedevents.Redis {
	return sharedevents.Redis{Client: client, Stream: Stream, Source: Source}
}
