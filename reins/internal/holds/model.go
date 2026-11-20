// Package holds is the one wheel: who holds the reins of a subject now. At
// most one holder at a time: a principal in it, from one of its devices; a
// brain in it, a driver's profile; or nobody, and the avatar is offline.
// Writing as an avatar needs its reins; a steward who does not hold them
// watches. Like stewardship it is out of the world: never shown to the
// subjects, never written into `subjects` or `threads`.
//
// Every change is published as reins.hold.changed on reins_events: the
// event only wakes (a driver reads the truth back, a principal's stream
// shows it), the table is the truth.
package holds

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The two kinds of holder, as `holder` is written.
const (
	PrincipalPrefix = "principal:"
	BrainPrefix     = "brain:"
)

// Hold is one subject's holder: `principal:<id>` on a device, or
// `brain:<profile>` (device empty).
type Hold struct {
	Subject uuid.UUID `json:"subject"`
	Holder  string    `json:"holder"`
	Device  string    `json:"device,omitempty"`
	Since   time.Time `json:"since"`
}

// Principal is the principal holding it, when one does.
func (h Hold) Principal() (string, bool) {
	p, ok := strings.CutPrefix(h.Holder, PrincipalPrefix)
	return p, ok && p != ""
}

// Profile is the brain's profile holding it, when one does.
func (h Hold) Profile() (string, bool) {
	p, ok := strings.CutPrefix(h.Holder, BrainPrefix)
	return p, ok && p != ""
}

// Brain is `current_brain`: who holds a subject's reins now, as one
// principal sees it from one device. Shown to principals only, never to
// characters, never kept in `subjects`.
type Brain struct {
	// Kind is `principal`, `brain` or `offline`.
	Kind string `json:"kind" example:"principal"`
	// Principal holding it (kind principal).
	Principal string `json:"principal,omitempty" example:"alice"`
	// Profile holding it (kind brain): a driver's profile.
	Profile string `json:"profile,omitempty" example:"fake"`
	// You: the principal asking holds it.
	You bool `json:"you,omitempty"`
	// Here: the principal asking holds it from the device asking.
	Here  bool       `json:"here,omitempty"`
	Since *time.Time `json:"since,omitempty"`
}

// View is a hold as `principal` sees it from `device`; nil is offline.
func View(h *Hold, principal, device string) Brain {
	if h == nil {
		return Brain{Kind: "offline"}
	}
	since := h.Since
	if p, ok := h.Principal(); ok {
		you := p == principal
		return Brain{Kind: "principal", Principal: p, You: you, Here: you && device != "" && h.Device == device, Since: &since}
	}
	if p, ok := h.Profile(); ok {
		return Brain{Kind: "brain", Profile: p, Since: &since}
	}
	return Brain{Kind: "offline"}
}

// Describe is a holder in words, for a refusal: "alice on another device",
// "brain fake", "nobody".
func Describe(h *Hold, principal string) string {
	if h == nil {
		return "nobody"
	}
	if p, ok := h.Principal(); ok {
		if p == principal {
			return "you on another device"
		}
		return p
	}
	if p, ok := h.Profile(); ok {
		return "brain " + p
	}
	return h.Holder
}

// HandRequest names the profile the reins go to.
type HandRequest struct {
	Profile string `json:"profile" example:"fake"`
}

// StopRequest names the subject whose reply in the thread is stopped.
type StopRequest struct {
	Subject uuid.UUID `json:"subject"`
}

var (
	// ErrNotSteward: the caller does not steer the subject.
	ErrNotSteward = errors.New("you do not steer this subject")
	// ErrNoDevice: a principal holds from a device, and none was named.
	ErrNoDevice = errors.New("no device: name it in X-Device-Id (or ?device=)")
	// ErrNoProfile: no brain by that profile.
	ErrNoProfile = errors.New("no such profile")
	// ErrNoBrains: nothing here says which brains there are (no BRAINS_URL).
	ErrNoBrains = errors.New("no brains here")
	// ErrNoBus: a stop has nowhere to be said (no REDIS_ADDR).
	ErrNoBus = errors.New("no bus to say it on (no REDIS_ADDR)")
	// ErrHeld: writing as an avatar whose reins someone else holds.
	ErrHeld = errors.New("held")
)

// Held is ErrHeld with who holds it, in words.
type Held struct{ By string }

func (h *Held) Error() string { return "held by " + h.By }
func (h *Held) Is(err error) bool {
	return err == ErrHeld
}
