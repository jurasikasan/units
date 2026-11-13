package task

import (
	"context"
	"log"
	"time"

	"github.com/jurasikasan/units/shared/events"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

// EventsStream is where the board says that a task moved or changed hands.
//
// An entry is a wake-up, not the truth. A consumer that acts on it reads the
// task back from the board and claims it with an expected `from`
// (StatusRequest.From), so a redelivered or stale entry costs one refused move
// and nothing else. That is also why the stream is capped (shared/redis
// perishableStreams): a trimmed entry is a missed wake-up, which the consumer's
// own look at the board recovers, never lost work.
const EventsStream = "board_events"

// EventSource is the CloudEvents source of every entry: this unit's path.
const EventSource = "/task_board"

// Event types (contracts/events.md: <resource>.<entity>.<event>). One stream
// for both, because a consumer that waits for "a TODO task assigned to me" can
// be woken by either: the task is approved while assigned, or assigned while
// approved.
const (
	EventStatus   = "task_board.task.status_changed"
	EventAssignee = "task_board.task.assignee_changed"
)

// Event is the board's state of a task right after a write, plus where its
// status came from. For an assignee change From and To are the same status:
// every entry says "the task is now in To, with Assignee", whatever changed.
type Event struct {
	Type     string
	TaskID   uuid.UUID
	Handle   Handle
	From     string
	To       string
	Assignee *string
	// Note is a line for the task's discussion — why a task was returned or
	// parked — said by NoteBy. The board keeps no thread; whoever holds the
	// discussion writes it.
	Note   string
	NoteBy string
}

// Data is the event's payload; the task's id is the envelope's subject. An
// unassigned task has "assignee": null, and note and note_by are present only
// when there is a note.
func (e Event) Data() map[string]any {
	d := map[string]any{
		"handle":   e.Handle.String(),
		"from":     e.From,
		"to":       e.To,
		"assignee": e.Assignee,
	}
	if e.Note != "" {
		d["note"] = e.Note
		d["note_by"] = e.NoteBy
	}
	return d
}

// publisher is what the service needs from the stream. Nil means nothing is
// published — the tests that are not about events build the Service that way.
type publisher interface {
	Publish(ctx context.Context, e Event) error
}

// RedisPublisher writes events to EventsStream in the CloudEvents envelope of
// contracts/events.md.
type RedisPublisher struct{ out events.Redis }

func NewRedisPublisher(rdb *redis.Client) *RedisPublisher {
	return &RedisPublisher{out: events.Redis{Client: rdb, Stream: EventsStream, Source: EventSource}}
}

func (p *RedisPublisher) Publish(ctx context.Context, e Event) error {
	return p.out.Publish(ctx, events.Event{Type: e.Type, Subject: e.TaskID.String(), Data: e.Data()})
}

// publishTimeout bounds one XADD. The write it reports has already happened,
// so a hung Redis must not hold the caller's answer for long.
const publishTimeout = 2 * time.Second

// publish reports a write that already happened. A failure is logged and
// swallowed: the row is changed, and an error here would make the caller
// retry a move that succeeded. The event is a wake-up; whoever misses it
// finds the task by looking at the board.
//
// WithoutCancel: a caller that hung up after the write still changed the
// board, and the event is owed for that change.
func (s *Service) publish(ctx context.Context, e Event) {
	if s.events == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	if err := s.events.Publish(ctx, e); err != nil {
		log.Printf("ERROR: publish %s event for %s: %v", e.Type, e.Handle, err)
	}
}
