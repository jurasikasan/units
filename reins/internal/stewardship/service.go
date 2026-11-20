package stewardship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
)

type Service struct {
	store    Store
	registry Registry
	// Welcome, when set, runs once a principal's personas are all made and
	// before it is settled, with the ids of `<login>` and
	// `<login>-assistant`: the assistant handed to the default brain, the
	// first thread opened. It must be idempotent: a run cut before the
	// principal is settled runs it again.
	Welcome func(ctx context.Context, principal string, login, assistant uuid.UUID) error
}

func NewService(store Store, registry Registry) *Service {
	return &Service{store: store, registry: registry}
}

// MySubjects is the subjects a principal steers, read from `subjects`
// (deleted ones left out). A principal first seen is given its personas
// first; a refusal to make them is a *Refusal and is logged.
func (s *Service) MySubjects(ctx context.Context, principal string) ([]Subject, error) {
	if err := s.provision(ctx, principal); err != nil {
		var ref *Refusal
		if errors.As(err, &ref) {
			log.Printf("reins: principal %q not provisioned: %s", principal, ref.Reason)
		}
		return nil, err
	}
	ids, err := s.store.Steered(ctx, principal)
	if err != nil {
		return nil, err
	}
	out := []Subject{}
	for _, id := range ids {
		sub, found, err := s.registry.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, sub)
		}
	}
	return out, nil
}

// provision makes what is missing of a principal's personas, once.
//
// Serialised per principal and checked again under the lock, so two first
// visits make each persona once. Each subject is recorded as soon as
// `subjects` made it, and the principal is settled only when all are there:
// a run cut half way completes on the next visit. Once settled, nothing is
// made again, so a persona handed to someone else is not made anew.
//
// A wanted nick is only ever matched against the subjects this principal
// already steers. One taken by anyone else is refused, never adopted: that
// would hand someone's avatar to whoever logs in with a matching name.
func (s *Service) provision(ctx context.Context, principal string) error {
	ok, err := s.store.Settled(ctx, principal)
	if err != nil || ok {
		return err
	}
	wanted, err := Wanted(principal)
	if err != nil {
		return err
	}
	return s.store.Provisioning(ctx, principal, func(l Locked) error {
		if ok, err := l.Settled(ctx); err != nil || ok {
			return err
		}
		ids, err := l.Steered(ctx)
		if err != nil {
			return err
		}
		have := map[string]uuid.UUID{}
		for _, id := range ids {
			sub, found, err := s.registry.Get(ctx, id)
			if err != nil {
				return err
			}
			if found {
				have[sub.Nick] = sub.ID
			}
		}
		for _, w := range wanted {
			if _, ok := have[w.Nick]; ok {
				continue
			}
			sub, err := s.registry.Create(ctx, w)
			switch {
			case errors.Is(err, ErrNickTaken):
				return &Refusal{Status: 409, Reason: fmt.Sprintf(
					"nick %q is taken by another subject; it is not adopted", w.Nick)}
			case errors.Is(err, ErrNickRefused):
				return &Refusal{Status: 422, Reason: err.Error()}
			case err != nil:
				return err
			}
			if err := l.Record(ctx, sub.ID); err != nil {
				return err
			}
			have[w.Nick] = sub.ID
			log.Printf("reins: principal %q steers new subject %s (%s)", principal, sub.ID, sub.Nick)
		}
		if s.Welcome != nil {
			if err := s.Welcome(ctx, principal, have[wanted[0].Nick], have[wanted[1].Nick]); err != nil {
				return err
			}
		}
		return l.Settle(ctx)
	})
}

// Handover moves the reins of a subject from the caller to another
// principal. Only a current steward may. Nothing is written to `subjects` or
// `threads`: who steers a subject is not a world event.
func (s *Service) Handover(ctx context.Context, caller string, subject uuid.UUID, to string) error {
	to = strings.TrimSpace(to)
	if to == "" || to == caller {
		return ErrBadHandover
	}
	if err := s.store.Handover(ctx, subject, caller, to); err != nil {
		return err
	}
	log.Printf("reins: subject %s handed over from %q to %q", subject, caller, to)
	return nil
}

// steward is ErrNotSteward unless the caller holds the reins of subject.
func (s *Service) steward(ctx context.Context, caller string, subject uuid.UUID) error {
	ok, err := s.store.Steers(ctx, caller, subject)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotSteward
	}
	return nil
}

// Subject is one subject the caller steers, as `subjects` has it.
// ErrNotSteward when the caller does not steer it, ErrNoSubject when
// `subjects` has it no more.
func (s *Service) Subject(ctx context.Context, caller string, subject uuid.UUID) (Subject, error) {
	if err := s.steward(ctx, caller, subject); err != nil {
		return Subject{}, err
	}
	sub, found, err := s.registry.Get(ctx, subject)
	if err != nil {
		return Subject{}, err
	}
	if !found {
		return Subject{}, ErrNoSubject
	}
	return sub, nil
}

// Change writes a steward's change of name, bio or preferences to
// `subjects`. Its PUT replaces a subject whole, so the subject is read as
// `subjects` sent it and only those three are touched: the nick, the type
// and any field reins does not know go back as they were stored.
// Preferences are merged key by key (see Change).
//
// Read, then written: two changes at once may lose one; the last write wins.
func (s *Service) Change(ctx context.Context, caller string, subject uuid.UUID, c Change) (Subject, error) {
	if err := s.steward(ctx, caller, subject); err != nil {
		return Subject{}, err
	}
	raw, found, err := s.registry.Raw(ctx, subject)
	if err != nil {
		return Subject{}, err
	}
	if !found {
		return Subject{}, ErrNoSubject
	}
	if c.Name != nil {
		raw["name"], _ = json.Marshal(*c.Name)
	}
	if c.Bio != nil {
		raw["bio"], _ = json.Marshal(*c.Bio)
	}
	if c.Preferences != nil {
		prefs := map[string]json.RawMessage{}
		if stored, ok := raw["preferences"]; ok && !isNull(stored) {
			if err := json.Unmarshal(stored, &prefs); err != nil {
				return Subject{}, fmt.Errorf("%w: the stored preferences are not an object", ErrBadChange)
			}
		}
		for k, v := range c.Preferences {
			if isNull(v) {
				delete(prefs, k)
			} else {
				prefs[k] = v
			}
		}
		raw["preferences"], _ = json.Marshal(prefs)
	}
	if _, err := s.registry.Replace(ctx, subject, raw); err != nil {
		return Subject{}, err
	}
	// Read back: the subject as `subjects` stored it (its JSONB orders the
	// preferences its own way), the same a GET answers.
	sub, found, err := s.registry.Get(ctx, subject)
	if err != nil {
		return Subject{}, err
	}
	if !found {
		return Subject{}, ErrNoSubject
	}
	log.Printf("reins: %q changed subject %s (%s)", caller, subject, sub.Nick)
	return sub, nil
}

func isNull(v json.RawMessage) bool {
	return len(v) == 0 || bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}
