package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"sort"

	"github.com/google/uuid"
)

// Stewards is what the world needs of stewardship: whom a principal steers.
type Stewards interface {
	Steered(ctx context.Context, principal string) ([]uuid.UUID, error)
	Steers(ctx context.Context, principal string, subject uuid.UUID) (bool, error)
}

var (
	// ErrNotSteward: the principal does not steer the avatar it would act as.
	ErrNotSteward = errors.New("you do not steer this avatar")
	// ErrNotMember: the avatar is not a member of the thread it would write in.
	ErrNotMember = errors.New("this avatar is not a member of the thread")
	// ErrNotYours: none of the principal's avatars is in the thread.
	ErrNotYours = errors.New("none of your avatars is in this thread")
	// ErrBad: a request that names no avatar, or content that is not a list.
	ErrBad = errors.New("bad request")
)

// Holding is what the world needs of the holds: whether the caller holds
// an avatar's reins from its device. Nil when it does; an error that is
// holds.ErrHeld, saying who does, when it does not.
type Holding interface {
	Holding(ctx context.Context, principal, device string, subject uuid.UUID) error
}

type Service struct {
	stewards Stewards
	journal  Journal
	holds    Holding
}

func NewService(stewards Stewards, journal Journal, holds Holding) *Service {
	return &Service{stewards: stewards, journal: journal, holds: holds}
}

// The kinds reins writes, ActivityStreams' as threads keeps them.
const (
	kindCreate = "create"
	kindJoin   = "join"
)

type newEntry struct {
	Sender  uuid.UUID       `json:"sender"`
	Kind    string          `json:"kind"`
	Content json.RawMessage `json:"content,omitempty"`
}

// MyThreads is the threads any avatar the principal steers is a member of,
// each with its name and members, newest first.
func (s *Service) MyThreads(ctx context.Context, principal string) ([]Thread, error) {
	avatars, err := s.stewards.Steered(ctx, principal)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	out := []Thread{}
	for _, a := range avatars {
		ts, err := s.journal.Member(ctx, a)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			full, err := s.journal.Get(ctx, t.ID)
			if errors.Is(err, ErrNoThread) {
				continue // deleted between the two reads
			}
			if err != nil {
				return nil, err
			}
			out = append(out, full)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Service) steers(ctx context.Context, principal string, avatar uuid.UUID) error {
	ok, err := s.stewards.Steers(ctx, principal, avatar)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotSteward, avatar)
	}
	return nil
}

// Open is a new thread opened by `as`, which joins it first, then each of
// `with`. For now every one of them is an avatar the principal steers:
// putting in someone else's is an invitation, a later task. Opening is an
// act of `as`: the caller holds its reins from its device.
func (s *Service) Open(ctx context.Context, principal, device string, as uuid.UUID, with []uuid.UUID) (Thread, error) {
	if as == uuid.Nil {
		return Thread{}, fmt.Errorf("%w: \"as\" names the avatar that opens the thread", ErrBad)
	}
	joining := []uuid.UUID{as}
	for _, w := range with {
		if !slices.Contains(joining, w) {
			joining = append(joining, w)
		}
	}
	for _, a := range joining {
		if err := s.steers(ctx, principal, a); err != nil {
			return Thread{}, err
		}
	}
	if err := s.holds.Holding(ctx, principal, device, as); err != nil {
		return Thread{}, err
	}
	t, err := s.open(ctx, as, joining)
	if err != nil {
		return Thread{}, err
	}
	log.Printf("reins: %q opened thread %s as %s with %v", principal, t.ID, as, joining[1:])
	return t, nil
}

// open mints a thread opened by `by`, and each of joining joins it.
func (s *Service) open(ctx context.Context, by uuid.UUID, joining []uuid.UUID) (Thread, error) {
	t, err := s.journal.Open(ctx, by)
	if err != nil {
		return Thread{}, err
	}
	for _, a := range joining {
		if _, err := s.journal.Append(ctx, t.ID, newEntry{Sender: a, Kind: kindJoin}); err != nil {
			return Thread{}, err
		}
	}
	return s.journal.Get(ctx, t.ID)
}

// FirstThread opens a principal's first thread: opened by its assistant,
// which joins it, then the login's avatar. Nothing is said: the brain in
// the assistant speaks first. Once only: a thread the assistant opened
// already is enough (provisioning cut half way runs this again).
func (s *Service) FirstThread(ctx context.Context, assistant, login uuid.UUID) error {
	ts, err := s.journal.Member(ctx, assistant)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.OpenedBy != nil && *t.OpenedBy == assistant {
			return nil
		}
	}
	t, err := s.open(ctx, assistant, []uuid.UUID{assistant, login})
	if err != nil {
		return err
	}
	log.Printf("reins: first thread %s opened by %s with %s", t.ID, assistant, login)
	return nil
}

// Say appends what `as` says or does in the thread: a create entry, its
// content as threads takes it. `as` must be an avatar the principal steers,
// whose reins it holds from this device, and a member of the thread.
func (s *Service) Say(ctx context.Context, principal, device string, thread, as uuid.UUID, content json.RawMessage) (json.RawMessage, error) {
	if as == uuid.Nil {
		return nil, fmt.Errorf("%w: \"as\" names the avatar that says it", ErrBad)
	}
	if err := s.steers(ctx, principal, as); err != nil {
		return nil, err
	}
	if err := s.holds.Holding(ctx, principal, device, as); err != nil {
		return nil, err
	}
	t, err := s.journal.Get(ctx, thread)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(t.Members, as) {
		return nil, ErrNotMember
	}
	return s.journal.Append(ctx, thread, newEntry{Sender: as, Kind: kindCreate, Content: content})
}

// Entries is a page of a thread one of the principal's avatars is in, as
// threads answers it. What a watcher sees is the channel itself: every
// entry, whoever sent it.
func (s *Service) Entries(ctx context.Context, principal string, thread uuid.UUID, query url.Values) (json.RawMessage, error) {
	if err := s.visible(ctx, principal, thread); err != nil {
		return nil, err
	}
	return s.journal.Entries(ctx, thread, query)
}

// visible is nil when one of the principal's avatars is in the thread.
func (s *Service) visible(ctx context.Context, principal string, thread uuid.UUID) error {
	t, err := s.journal.Get(ctx, thread)
	if err != nil {
		return err
	}
	avatars, err := s.stewards.Steered(ctx, principal)
	if err != nil {
		return err
	}
	if !anyIn(avatars, t.Members) {
		return ErrNotYours
	}
	return nil
}

func anyIn(avatars, members []uuid.UUID) bool {
	for _, a := range avatars {
		if slices.Contains(members, a) {
			return true
		}
	}
	return false
}
