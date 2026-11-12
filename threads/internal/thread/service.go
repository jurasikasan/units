package thread

import (
	"context"
	"errors"

	"github.com/google/uuid"
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
}

// NewService wraps a repository.
func NewService(repo Repository) *DefaultService {
	return &DefaultService{repo: repo}
}

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
	return e, nil
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
