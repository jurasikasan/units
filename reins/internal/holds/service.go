package holds

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The events reins publishes on reins_events (CONTRACT.md, section 5).
const (
	// Stream is reins' own stream on the bus.
	Stream = "reins_events"
	Source = "/reins"
	// HoldChanged: a subject's holder changed; data subject, holder, device.
	HoldChanged = "reins.hold.changed"
	// ReplyStop: a steward asks the brain writing in a thread to stop;
	// data thread, subject, by.
	ReplyStop = "reins.reply.stop"
)

// Stewards is what holds needs of stewardship: who may take a subject up.
type Stewards interface {
	Steers(ctx context.Context, principal string, subject uuid.UUID) (bool, error)
}

// Publisher puts one event on reins_events.
type Publisher interface {
	Publish(ctx context.Context, typ, subject string, data map[string]any) error
}

// Brains is what the driver says of its profiles, relayed.
type Brains interface {
	Profiles(ctx context.Context) ([]string, error)
}

type Service struct {
	store    Store
	stewards Stewards
	bus      Publisher
	brains   Brains
}

// NewService: bus may be nil (no REDIS_ADDR: changes are not announced),
// brains may be nil (no BRAINS_URL: no profiles to hand to).
func NewService(store Store, stewards Stewards, bus Publisher, brains Brains) *Service {
	return &Service{store: store, stewards: stewards, bus: bus, brains: brains}
}

func (s *Service) steward(ctx context.Context, principal string, subject uuid.UUID) error {
	ok, err := s.stewards.Steers(ctx, principal, subject)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotSteward
	}
	return nil
}

// changed announces a subject's new holder ("" for nobody). The event only
// wakes: a reader that misses it reads the truth on its next look.
func (s *Service) changed(ctx context.Context, h Hold) {
	if s.bus == nil {
		return
	}
	data := map[string]any{"subject": h.Subject, "holder": h.Holder}
	if h.Device != "" {
		data["device"] = h.Device
	}
	if err := s.bus.Publish(ctx, HoldChanged, h.Subject.String(), data); err != nil {
		log.Printf("reins: announce the hold of %s: %v", h.Subject, err)
	}
}

func (s *Service) set(ctx context.Context, h Hold) error {
	changed, err := s.store.Set(ctx, h)
	if err != nil {
		return err
	}
	if changed {
		log.Printf("reins: %s holds %s", h.Holder, h.Subject)
		s.changed(ctx, h)
	}
	return nil
}

// Take gives the reins of a subject to the caller on its device. Any
// steward may, from anyone: stewards are equal, and taking is the kill
// switch. The former holder learns it on its stream.
func (s *Service) Take(ctx context.Context, principal, device string, subject uuid.UUID) (Brain, error) {
	if device == "" {
		return Brain{}, ErrNoDevice
	}
	if err := s.steward(ctx, principal, subject); err != nil {
		return Brain{}, err
	}
	h := Hold{Subject: subject, Holder: PrincipalPrefix + principal, Device: device}
	if err := s.set(ctx, h); err != nil {
		return Brain{}, err
	}
	// The grace counts from now, until the device's stream is open.
	if err := s.store.Seen(ctx, principal, device); err != nil {
		log.Printf("reins: %v", err)
	}
	return s.View(ctx, principal, device, subject)
}

// GiveBack gives the reins of a subject to nobody: the avatar is offline.
func (s *Service) GiveBack(ctx context.Context, principal string, subject uuid.UUID) error {
	if err := s.steward(ctx, principal, subject); err != nil {
		return err
	}
	cleared, err := s.store.Clear(ctx, subject)
	if err != nil {
		return err
	}
	if cleared {
		log.Printf("reins: %q gave back %s: nobody holds it", principal, subject)
		s.changed(ctx, Hold{Subject: subject})
	}
	return nil
}

// Release is a brain letting go of its own hold: nobody holds the subject
// when brain:<profile> still does, and the avatar is offline until a
// steward takes it or hands it on. When someone else holds it now (taken
// meanwhile) nothing changes: a late letting go never drops another
// holder. Says whether it let go.
func (s *Service) Release(ctx context.Context, subject uuid.UUID, profile string) (bool, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return false, ErrNoProfile
	}
	released, err := s.store.ClearIf(ctx, subject, BrainPrefix+profile)
	if err != nil {
		return false, err
	}
	if released {
		log.Printf("reins: brain %s let go of %s: nobody holds it", profile, subject)
		s.changed(ctx, Hold{Subject: subject})
	}
	return released, nil
}

// Hand gives the reins of a subject to a brain: one of the profiles the
// driver says it has.
func (s *Service) Hand(ctx context.Context, principal string, subject uuid.UUID, profile string) (Brain, error) {
	profile = strings.TrimSpace(profile)
	if err := s.steward(ctx, principal, subject); err != nil {
		return Brain{}, err
	}
	profiles, err := s.Profiles(ctx)
	if err != nil {
		return Brain{}, err
	}
	if !slices.Contains(profiles, profile) {
		return Brain{}, fmt.Errorf("%w: %q", ErrNoProfile, profile)
	}
	if err := s.set(ctx, Hold{Subject: subject, Holder: BrainPrefix + profile}); err != nil {
		return Brain{}, err
	}
	return s.View(ctx, principal, "", subject)
}

// Default hands a subject nobody holds to a brain; one somebody holds is
// left as it is. Provisioning's: `<login>-assistant` to DEFAULT_BRAIN.
func (s *Service) Default(ctx context.Context, subject uuid.UUID, profile string) error {
	h := Hold{Subject: subject, Holder: BrainPrefix + profile}
	made, err := s.store.SetIfNone(ctx, h)
	if err != nil {
		return err
	}
	if made {
		log.Printf("reins: %s holds %s from the start", h.Holder, subject)
		s.changed(ctx, h)
	}
	return nil
}

// Profiles is the brains there are, as the driver says them.
func (s *Service) Profiles(ctx context.Context) ([]string, error) {
	if s.brains == nil {
		return nil, ErrNoBrains
	}
	return s.brains.Profiles(ctx)
}

// Stop asks the brain writing as subject in the thread to stop: a steward
// of that subject may. What it wrote so far is the driver's to journal.
func (s *Service) Stop(ctx context.Context, principal string, thread, subject uuid.UUID) error {
	if err := s.steward(ctx, principal, subject); err != nil {
		return err
	}
	if s.bus == nil {
		return ErrNoBus
	}
	log.Printf("reins: %q stops %s writing in %s", principal, subject, thread)
	return s.bus.Publish(ctx, ReplyStop, thread.String(), map[string]any{"thread": thread, "subject": subject, "by": principal})
}

// Holding is nil when the caller holds the subject's reins from its device,
// else *Held: writing as an avatar needs its reins.
func (s *Service) Holding(ctx context.Context, principal, device string, subject uuid.UUID) error {
	h, ok, err := s.store.Get(ctx, subject)
	if err != nil {
		return err
	}
	if ok {
		if p, mine := h.Principal(); mine && p == principal && device != "" && h.Device == device {
			return nil
		}
		return &Held{By: Describe(&h, principal)}
	}
	return &Held{By: Describe(nil, principal) + ": take the reins first"}
}

// View is one subject's current_brain as the caller sees it.
func (s *Service) View(ctx context.Context, principal, device string, subject uuid.UUID) (Brain, error) {
	h, ok, err := s.store.Get(ctx, subject)
	if err != nil {
		return Brain{}, err
	}
	if !ok {
		return View(nil, principal, device), nil
	}
	return View(&h, principal, device), nil
}

// Views is current_brain of each subject, as the caller sees it.
func (s *Service) Views(ctx context.Context, principal, device string, subjects []uuid.UUID) (map[uuid.UUID]Brain, error) {
	hs, err := s.store.Of(ctx, subjects)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]Brain{}
	for _, id := range subjects {
		if h, ok := hs[id]; ok {
			out[id] = View(&h, principal, device)
		} else {
			out[id] = View(nil, principal, device)
		}
	}
	return out, nil
}

// Brains is every hold of a brain: what a driver reads to know whom its
// profiles hold.
func (s *Service) Brains(ctx context.Context) ([]Hold, error) { return s.store.Brains(ctx) }

// Seen marks a device of a principal as there now: its stream is open.
func (s *Service) Seen(ctx context.Context, principal, device string) error {
	return s.store.Seen(ctx, principal, device)
}

// Sweep lets go of every principal's hold whose device has been gone
// longer than grace; each avatar let go is offline, and said so.
func (s *Service) Sweep(ctx context.Context, grace time.Duration) {
	gone, err := s.store.Lapse(ctx, grace)
	if err != nil {
		log.Printf("reins: lapse holds: %v", err)
		return
	}
	for _, h := range gone {
		log.Printf("reins: %s lapsed on %s (device gone over %s): nobody holds it", h.Holder, h.Subject, grace)
		s.changed(ctx, Hold{Subject: h.Subject})
	}
}

// Lapsing sweeps every `every` until ctx ends.
func (s *Service) Lapsing(ctx context.Context, grace, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx, grace)
		}
	}
}
