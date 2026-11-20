package stewardship

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
)

type Service struct {
	store    Store
	registry Registry
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
		have := map[string]bool{}
		for _, id := range ids {
			sub, found, err := s.registry.Get(ctx, id)
			if err != nil {
				return err
			}
			if found {
				have[sub.Nick] = true
			}
		}
		for _, w := range wanted {
			if have[w.Nick] {
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
			log.Printf("reins: principal %q steers new subject %s (%s, %s)", principal, sub.ID, sub.Nick, sub.SubjectType)
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
