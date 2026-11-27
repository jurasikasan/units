package brain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Defaults of the knobs (unit.yaml config, CONTRACT §5).
const (
	DefaultPort         = 8080
	DefaultGroup        = "brain"
	DefaultKeep         = 4 * time.Second
	DefaultBufferMax    = 32
	DefaultSettle       = 500 * time.Millisecond
	DefaultMaxWait      = 3 * time.Second
	DefaultBind         = 200 * time.Millisecond
	DefaultRecall       = 20
	DefaultModelURL     = "http://litellm:4000/v1"
	DefaultModel        = "brain"
	DefaultModelTimeout = 60 * time.Second
	DefaultActTimeout   = 90 * time.Second
	DefaultThreadsURL   = "http://threads:8085"
	DefaultArtifactURL  = "http://artifact_store:8094"
)

// ParseList splits "a,b, c" into its non-empty items.
func ParseList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// pairs splits "a:x,b:y" into its pairs; an item without a colon has key "".
func pairs(s string) ([][2]string, error) {
	var out [][2]string
	for _, item := range ParseList(s) {
		k, v, ok := strings.Cut(item, ":")
		if !ok {
			k, v = "", item
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if v == "" {
			return nil, fmt.Errorf("%q has no value", item)
		}
		out = append(out, [2]string{k, v})
	}
	return out, nil
}

// ParseGate reads BRAIN_GATE: "sight:closed,hearing:open". Every channel not
// named is open.
func ParseGate(s string) (map[string]bool, error) {
	ps, err := pairs(s)
	if err != nil {
		return nil, err
	}
	closed := map[string]bool{}
	for _, p := range ps {
		if p[0] == "" {
			return nil, fmt.Errorf("%q names no channel", p[1])
		}
		switch p[1] {
		case "closed":
			closed[p[0]] = true
		case "open":
			delete(closed, p[0])
		default:
			return nil, fmt.Errorf("%s: %q is neither open nor closed", p[0], p[1])
		}
	}
	return closed, nil
}

// ParseKeep reads BRAIN_KEEP: "hearing:4s,sight:1s", and a bare duration
// ("4s", or "4s,sight:1s") for every channel not named; def when none is bare.
func ParseKeep(s string, def time.Duration) (map[string]time.Duration, time.Duration, error) {
	ps, err := pairs(s)
	if err != nil {
		return nil, 0, err
	}
	keep := map[string]time.Duration{}
	for _, p := range ps {
		d, err := time.ParseDuration(p[1])
		if err != nil || d <= 0 {
			return nil, 0, fmt.Errorf("%q is not a positive duration", p[1])
		}
		if p[0] == "" {
			def = d
		} else {
			keep[p[0]] = d
		}
	}
	return keep, def, nil
}

// ParseSenders reads BRAIN_SENDERS: "hearing:<uuid>,sight:<uuid>", the
// subject each channel writes into memory as.
func ParseSenders(s string) (map[string]uuid.UUID, error) {
	ps, err := pairs(s)
	if err != nil {
		return nil, err
	}
	out := map[string]uuid.UUID{}
	for _, p := range ps {
		if p[0] == "" {
			return nil, fmt.Errorf("%q names no channel", p[1])
		}
		id, err := uuid.Parse(p[1])
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("%s: %q is not a subject id", p[0], p[1])
		}
		out[p[0]] = id
	}
	return out, nil
}
