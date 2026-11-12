package thread

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// ErrInvalid wraps every refusal of an entry's shape; the handler answers 400.
var ErrInvalid = errors.New("invalid entry")

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// blockFields: which fields each block type may carry, and which of them it
// must. A field set on a block of another type is refused.
var blockFields = map[string]struct{ allowed, required []string }{
	BlockText:       {[]string{"text"}, []string{"text"}},
	BlockAudio:      {[]string{"url", "media_type", "sha256", "transcript"}, []string{"url", "media_type", "sha256"}},
	BlockImage:      {[]string{"url", "media_type", "sha256"}, []string{"url", "media_type", "sha256"}},
	BlockToolCall:   {[]string{"id", "name", "args"}, []string{"id", "name", "args"}},
	BlockToolResult: {[]string{"call_id", "text", "is_error"}, []string{"call_id"}},
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// validateEntry holds an entry to section 1 of CONTRACT.md.
func validateEntry(e NewEntry) error {
	if e.Sender == uuid.Nil {
		return invalid("sender is required")
	}
	switch e.Kind {
	case KindCreate:
		if e.Name != "" {
			return invalid("a create entry carries no name")
		}
		if len(e.Content) == 0 {
			return invalid("a create entry carries at least one block")
		}
		for i, b := range e.Content {
			if err := validateBlock(b); err != nil {
				return fmt.Errorf("block %d: %w", i, err)
			}
		}
	case KindJoin, KindLeave:
		if e.Name != "" || len(e.Content) > 0 {
			return invalid("a %s entry carries nothing", e.Kind)
		}
	case KindUpdate:
		if len(e.Content) > 0 {
			return invalid("an update entry carries no content")
		}
		if e.Name == "" {
			return invalid("an update entry carries the new name")
		}
	default:
		return invalid("unknown kind %q", e.Kind)
	}
	return nil
}

func validateBlock(b Block) error {
	rule, ok := blockFields[b.Type]
	if !ok {
		return invalid("unknown block type %q", b.Type)
	}
	set := setFields(b)
	allowed := map[string]bool{}
	for _, f := range rule.allowed {
		allowed[f] = true
	}
	for _, f := range set {
		if !allowed[f] {
			return invalid("a %s block has no field %q", b.Type, f)
		}
	}
	present := map[string]bool{}
	for _, f := range set {
		present[f] = true
	}
	for _, f := range rule.required {
		if !present[f] {
			return invalid("a %s block needs %q", b.Type, f)
		}
	}
	if b.SHA256 != "" && !sha256Pattern.MatchString(b.SHA256) {
		return invalid("sha256 is 64 lower-case hex digits")
	}
	if b.Args != nil {
		t := bytes.TrimSpace(b.Args)
		if len(t) == 0 || t[0] != '{' {
			return invalid("args is a JSON object")
		}
	}
	return nil
}

// setFields: the JSON names of the block's fields that are not empty.
func setFields(b Block) []string {
	var out []string
	add := func(name string, set bool) {
		if set {
			out = append(out, name)
		}
	}
	add("text", b.Text != "")
	add("url", b.URL != "")
	add("media_type", b.MediaType != "")
	add("sha256", b.SHA256 != "")
	add("transcript", b.Transcript != "")
	add("id", b.ID != "")
	add("name", b.Name != "")
	add("args", len(b.Args) > 0)
	add("call_id", b.CallID != "")
	add("is_error", b.IsError)
	return out
}
