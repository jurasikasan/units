package task

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Handle is a task's spoken name — ABC-12. The id remains the address for
// machines; this exists because people talk about these things out loud all day
// and a UUID cannot be said. The eight-character id prefix can be read aloud but
// carries no order, and order is half the point: "ABC-12 is older than ABC-15"
// is a sentence you can act on.
//
// It is a pair rather than a number because projects are a context, not a
// codebase, and there will be more than one. Numbering settled after the second
// project exists means renumbering handles people have already said.
type Handle struct {
	Project string
	Number  int64
}

// keyPattern mirrors the CHECK on projects.key. Two copies on purpose, the
// same way the status vocabulary is: the CHECK is what no writer can route
// around, this is what answers a caller with a sentence instead of a 500.
var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

// ValidProjectKey reports whether k can be a project key.
func ValidProjectKey(k string) bool { return keyPattern.MatchString(k) }

func (h Handle) String() string {
	return h.Project + "-" + strconv.FormatInt(h.Number, 10)
}

// MarshalJSON renders the handle as the string people say, not as the two
// columns it is stored in. A client that had to assemble "ABC-12" itself would
// eventually assemble it differently.
func (h Handle) MarshalJSON() ([]byte, error) { return json.Marshal(h.String()) }

// UnmarshalJSON is the other half of that decision: it reads back exactly the
// string MarshalJSON writes, and nothing else. The object form with project and
// number is refused rather than accepted, because two ways to spell the same
// value mean one of them will stop being the one people say, and the split-on-
// the-dash validation that ParseHandle applies is the only check a caller can
// rely on. A garbage string is an error, not a silently zero handle — a zero
// handle looks like a value that happened to be omitted, and nothing upstream
// can tell the difference.
func (h *Handle) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: a handle is a string, the shape it is written in — got %s",
			ErrInvalid, string(data))
	}
	parsed, err := ParseHandle(raw)
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// ParseHandle reads a spoken name. The split is on the LAST dash so that the
// shape stays obvious, though the key pattern forbids dashes anyway — if that
// ever relaxes, this does not quietly start taking the wrong half.
func ParseHandle(s string) (Handle, error) {
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, "-")
	if i <= 0 || i == len(s)-1 {
		return Handle{}, fmt.Errorf("%w: %q is not a handle — the shape is KEY-123",
			ErrInvalid, s)
	}
	key, digits := s[:i], s[i+1:]
	if !ValidProjectKey(key) {
		return Handle{}, fmt.Errorf("%w: %q is not a project key — uppercase "+
			"letters and digits, 2 to 10 characters", ErrInvalid, key)
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 1 {
		return Handle{}, fmt.Errorf("%w: %q is not a task number", ErrInvalid, digits)
	}
	return Handle{Project: key, Number: n}, nil
}

// Project is a line of work with its own numbering. Almost empty on purpose:
// this is also where per-project settings would eventually hang, and the row
// existing now means that work will not have to invent a home.
type Project struct {
	Key  string `json:"key"`
	Name string `json:"name"`

	// Components is the project's own list: a task's component is
	// one of these, and a list that removes a value a task still carries is
	// refused. Always non-nil — the column is NOT NULL DEFAULT '{}', so a
	// project without components answers [] and never null.
	Components []string `json:"components"`
}

// CreateProjectRequest is what a caller may set.
type CreateProjectRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// ComponentsRequest is the body of PUT /api/v1/projects/{key}/components.
// It replaces the project's list in full — that is the endpoint
// for removing a component, and there is deliberately no additive form: a
// registry you can only grow is a label. Blanks and duplicates are the same
// spelling problem as with labels, so the service normalises them the same
// way rather than trusting the storage.
type ComponentsRequest struct {
	Components []string `json:"components"`
}

// Validate normalises the key to upper case before checking it, because the
// difference between "abc" and "ABC" is a shouting convention rather than two
// projects, and refusing the lower-case form teaches nothing.
func (r *CreateProjectRequest) Validate() error {
	r.Key = strings.ToUpper(strings.TrimSpace(r.Key))
	if !ValidProjectKey(r.Key) {
		return fmt.Errorf("%w: key must be 2 to 10 characters, uppercase letters "+
			"and digits, starting with a letter — it is said out loud dozens of "+
			"times a day, which is what the length cap is for", ErrInvalid)
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalid)
	}
	return nil
}
