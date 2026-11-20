// Package stewardship is which principal holds the reins of which subject:
// a relation out of the world, between an account at the door and a
// character in `subjects`. Nothing here is a world event, and nothing here is
// written into `subjects` beyond the two personas a principal first seen is
// given.
package stewardship

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/jurasikasan/units/reins/internal/holds"
)

// Subject is a subject as `subjects` keeps it, passed on as it came.
type Subject struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Nick        string          `json:"nick"`
	Bio         string          `json:"bio,omitempty"`
	Preferences json.RawMessage `json:"preferences,omitempty" swaggertype:"object"`
	// CurrentBrain is who holds its reins now, as the principal asking sees
	// it from its device: reins' own, added to what `subjects` sent, never
	// written there and never shown to a character.
	CurrentBrain *holds.Brain `json:"current_brain,omitempty"`
}

// NewSubject is what reins asks `subjects` to make.
type NewSubject struct {
	Nick string `json:"nick"`
	Name string `json:"name"`
}

// Change is what a steward may change of a subject: its name, its bio, its
// preferences. A field left out is kept. Preferences are merged key by key
// into the stored ones: a key given replaces that key, a key given as null
// takes it out, a key not given is kept, so one reader's keys survive
// another's change. The nick is not here: it never changes.
type Change struct {
	Name        *string                    `json:"name,omitempty" example:"Alice"`
	Bio         *string                    `json:"bio,omitempty" example:"Writes the release notes."`
	Preferences map[string]json.RawMessage `json:"preferences,omitempty" swaggertype:"object"`
}

// HandoverRequest names the principal who takes the reins.
type HandoverRequest struct {
	To string `json:"to" example:"bob"`
}

// AssistantSuffix makes the second persona's nick from the login.
const AssistantSuffix = "-assistant"

// The nick rules as `subjects` holds them (subjects/CONTRACT.md); checked
// here first so that a login that cannot become a nick is refused with a
// reason before anything is made.
var nickPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// Wanted is what a principal first seen is given: `<login>` and
// `<login>-assistant`, names empty. A login that cannot become both nicks is
// refused, never bent into one: a nick made from a changed login would read
// as somebody else's.
func Wanted(login string) ([]NewSubject, error) {
	if !nickPattern.MatchString(login) {
		return nil, &Refusal{Status: 422, Reason: fmt.Sprintf(
			"login %q cannot be a nick: a nick is 2-32 characters of a-z, 0-9, - and _, starting with a letter", login)}
	}
	assistant := login + AssistantSuffix
	if !nickPattern.MatchString(assistant) {
		return nil, &Refusal{Status: 422, Reason: fmt.Sprintf(
			"login %q is too long for its assistant's nick %q: a nick is at most 32 characters", login, assistant)}
	}
	return []NewSubject{
		{Nick: login},
		{Nick: assistant},
	}, nil
}

// Refusal is a principal's provisioning refused for a reason the principal
// can be told: a nick taken by another subject (409), a login that breaks the
// nick rules (422).
type Refusal struct {
	Status int
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

var (
	// ErrNotSteward: the caller does not hold the reins of that subject.
	ErrNotSteward = errors.New("you do not steer this subject")
	// ErrNoSubject: the caller steers it, but `subjects` has it no more.
	ErrNoSubject = errors.New("no such subject")
	// ErrBadChange: preferences that cannot be merged into the stored ones.
	ErrBadChange = errors.New("bad change")
	// ErrBadHandover: no one to hand over to, or the caller themself.
	ErrBadHandover = errors.New(`"to" must name another principal`)
	// ErrNickTaken: `subjects` answered 409 for a nick.
	ErrNickTaken = errors.New("nick is taken")
	// ErrNickRefused: `subjects` answered 400 for a nick.
	ErrNickRefused = errors.New("nick refused by subjects")
)
