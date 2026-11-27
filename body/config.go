// Package body is a socket between the world and a head (CONTRACT.md): it
// opens its own senses and serves where they take input, republishes its own
// perception, and only its own, onto its stream, and offers its acts as MCP
// tools to whoever drives it. It knows nothing of a head.
package body

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Version is stamped at build time (-ldflags "-X github.com/jurasikasan/units/body.Version=…").
var Version = "dev"

// The streams a body reads: the senses' and the ability's own, named in
// their contracts.
const (
	HearingStream = "hearing_events"
	SightStream   = "sight_events"
	SpeechStream  = "speech_events"
)

// What a body's stream, its source and its group are called, {name} its
// BODY_NAME: body_a_events, /bodies/a (acts) and /bodies/a/hearing (percepts).
const (
	streamTemplate = "body_{name}_events"
	sourceTemplate = "/bodies/{name}"
	groupTemplate  = "body_{name}"
)

// Defaults of the knobs (unit.yaml config, CONTRACT §5).
const (
	DefaultPort         = 8080
	DefaultSpeakTimeout = 60 * time.Second
	DefaultRetry        = 2 * time.Second
	// DefaultHostGroup is a host's consumer group when it carries bodies
	// made only through its API; one made from the env keeps body_{name}.
	DefaultHostGroup = "bodies"
)

// Config is a body's: who it is, what it is to an observer, its senses, its
// mouth and who is within earshot.
type Config struct {
	ID          uuid.UUID
	Name        string
	Description string
	// Stream is where its perception and acts go; Group its consumer group on
	// the senses' and the ability's streams.
	Stream, Group string
	// The units' MCP endpoints; an empty one is a sense (or a mouth) the body
	// does not have.
	HearingURL, SightURL, SpeakingURL string
	// Language is passed to the senses as the session's language; empty lets them guess.
	Language string
	// Voice is speaking's voice_name; required with a mouth.
	Voice string
	// Earshot is other bodies' base URLs: who hears what this one says.
	Earshot []string
	// SpeakTimeout bounds how long speak_aloud waits for its phrase to be spoken.
	SpeakTimeout time.Duration
	// Retry is the pause before a sense that would not open is asked again.
	Retry time.Duration
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// CheckName: a name goes into a stream's name and a source path, so it is
// kept to lower-case letters, digits, '_' and '-'.
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q: a name is lower-case letters, digits, '_' and '-', at most 63", name)
	}
	return nil
}

func fill(template, name string) string { return strings.ReplaceAll(template, "{name}", name) }

// StreamFor, SourceFor and GroupFor are a body's defaults by its name.
func StreamFor(name string) string { return fill(streamTemplate, name) }
func SourceFor(name string) string { return fill(sourceTemplate, name) }
func GroupFor(name string) string  { return fill(groupTemplate, name) }

// DerivedID is a body's id when none is configured: the same on every start.
func DerivedID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("urn:units:body:"+name))
}

// ParseEarshot reads BODY_EARSHOT: other bodies' base URLs, comma-separated.
func ParseEarshot(s string) ([]string, error) {
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimRight(strings.TrimSpace(item), "/")
		if item == "" {
			continue
		}
		u, err := url.Parse(item)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%q is not an http(s) base URL", item)
		}
		out = append(out, item)
	}
	return out, nil
}
