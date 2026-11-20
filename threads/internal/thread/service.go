package thread

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/jurasikasan/units/threads/internal/events"
)

// Limits on one page of entries.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// ErrBadPage: after and before together, or a limit out of range.
var ErrBadPage = errors.New("bad page")

// Service is what the handler calls.
type Service interface {
	Open(ctx context.Context, n NewThread) (*Thread, error)
	Get(ctx context.Context, id uuid.UUID) (*Thread, error)
	List(ctx context.Context, f Filter) ([]Thread, error)
	Delete(ctx context.Context, id uuid.UUID) error
	Append(ctx context.Context, thread uuid.UUID, n NewEntry) (*Entry, error)
	Entries(ctx context.Context, thread uuid.UUID, q PageQuery) ([]Entry, error)
}

// PageQuery is a page as the caller asked for it; nil means not given.
// Tail asks for the last Limit entries.
type PageQuery struct {
	After, Before, Limit *int64
	Tail                 bool
}

// DefaultService is Service over a Repository.
type DefaultService struct {
	repo Repository
	pub  events.Publisher
}

// NewService wraps a repository; it announces nothing until given a publisher.
func NewService(repo Repository) *DefaultService {
	return &DefaultService{repo: repo}
}

// Announcing makes each append publish threads.entry.appended through pub;
// nil leaves the service silent.
func (s *DefaultService) Announcing(pub events.Publisher) *DefaultService {
	s.pub = pub
	return s
}

// publishTimeout bounds how long an append waits on a Redis that does not
// answer; the entry is stored by then and the publish is only a wake-up.
const publishTimeout = time.Second

// Open mints the id and stores the thread.
func (s *DefaultService) Open(ctx context.Context, n NewThread) (*Thread, error) {
	t := &Thread{ID: uuid.New(), Parent: n.Parent, OpenedBy: n.OpenedBy}
	if err := s.repo.CreateThread(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// Get reads a thread with its name and members.
func (s *DefaultService) Get(ctx context.Context, id uuid.UUID) (*Thread, error) {
	return s.repo.GetThread(ctx, id)
}

// List reads threads, newest first.
func (s *DefaultService) List(ctx context.Context, f Filter) ([]Thread, error) {
	return s.repo.ListThreads(ctx, f)
}

// Delete removes a thread, its entries and its children.
func (s *DefaultService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteThread(ctx, id)
}

// Append checks the entry and stores it at the end of the thread.
func (s *DefaultService) Append(ctx context.Context, thread uuid.UUID, n NewEntry) (*Entry, error) {
	if err := validateEntry(n); err != nil {
		return nil, err
	}
	content := n.Content
	if content == nil {
		content = []Block{}
	}
	e := &Entry{ID: uuid.New(), Thread: thread, Sender: n.Sender, Kind: n.Kind, Content: content, Name: n.Name}
	if err := s.repo.Append(ctx, e); err != nil {
		return nil, err
	}
	s.announce(ctx, e)
	return e, nil
}

// announce publishes the append. A failure is logged and dropped: the journal
// is the truth, and a reader that missed the event catches up by seq. The
// publish outlives a caller that hung up, since the entry is stored already.
func (s *DefaultService) announce(ctx context.Context, e *Entry) {
	if s.pub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	err := s.pub.Publish(ctx, events.Event{
		Type:    events.EntryAppended,
		Subject: e.Thread.String(),
		Data: map[string]any{
			"thread": e.Thread.String(),
			"seq":    e.Seq,
			"sender": e.Sender.String(),
			"kind":   e.Kind,
		},
		Traceparent: traceparent(ctx),
		Time:        e.At,
	})
	if err != nil {
		log.Printf("threads: entry %s seq %d appended, not announced: %v", e.Thread, e.Seq, err)
	}
}

type traceKey struct{}

// WithTraceparent carries the W3C traceparent of the request that caused an
// append on to its event.
func WithTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, tp)
}

func traceparent(ctx context.Context) string {
	tp, _ := ctx.Value(traceKey{}).(string)
	return tp
}

// Entries reads a page of a thread's entries.
func (s *DefaultService) Entries(ctx context.Context, thread uuid.UUID, q PageQuery) ([]Entry, error) {
	p, err := page(q)
	if err != nil {
		return nil, err
	}
	return s.repo.Entries(ctx, thread, p)
}

func page(q PageQuery) (Page, error) {
	p := Page{Limit: DefaultLimit}
	if q.Limit != nil {
		if *q.Limit < 1 || *q.Limit > MaxLimit {
			return p, ErrBadPage
		}
		p.Limit = int(*q.Limit)
	}
	given := 0
	for _, set := range []bool{q.After != nil, q.Before != nil, q.Tail} {
		if set {
			given++
		}
	}
	switch {
	case given > 1:
		return p, ErrBadPage
	case q.Tail:
		p.Tail = true
	case q.Before != nil:
		if *q.Before < 1 {
			return p, ErrBadPage
		}
		p.Before = *q.Before
	case q.After != nil:
		if *q.After < 0 {
			return p, ErrBadPage
		}
		p.After = *q.After
	}
	return p, nil
}
