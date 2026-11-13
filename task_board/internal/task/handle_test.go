package task

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHandleJSONRoundTrip is the contract a Go client can rely on: what
// MarshalJSON wrote, UnmarshalJSON reads back, value for value. It is pinned
// against the string on the wire, not just against the parsed value, because
// the whole point of marshalling a pair as one word is that the word is stable.
func TestHandleJSONRoundTrip(t *testing.T) {
	for _, h := range []Handle{
		{Project: "ABC", Number: 1},
		{Project: "ABC", Number: 412},
		{Project: "XYZ2", Number: 99},
		{Project: "A1B2C3D4E5", Number: 7},
	} {
		b, err := json.Marshal(h)
		require.NoError(t, err, "%v should marshal", h)
		require.JSONEq(t, `"`+h.String()+`"`, string(b),
			"%v must render as the string people say", h)

		var got Handle
		require.NoError(t, json.Unmarshal(b, &got), "%s should read back", b)
		require.Equal(t, h, got, "%s did not come back as itself", b)
	}
}

// TestTaskJSONRoundTripHandle: the handle
// arrives as the string "ABC-412" while the struct holds two fields, and a Go
// client that decodes a Task must land on the handle it sent.
func TestTaskJSONRoundTripHandle(t *testing.T) {
	id := uuid.New()
	origin := "chat-1"
	chat := "chat-2"
	reporter := "reporter"
	created := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

	task := Task{
		ID:         id,
		Handle:     Handle{Project: "ABC", Number: 412},
		ProjectKey: "ABC",
		Goal:       "round trip",
		Status:     StatusTodo,
		Origin:     &origin,
		Discussion: &chat,
		Reporter:   reporter,
		BlockedBy: []Handle{
			{Project: "ABC", Number: 1},
			{Project: "XYZ2", Number: 9},
		},
		CreatedAt: created,
		UpdatedAt: created,
	}

	b, err := json.Marshal(task)
	require.NoError(t, err)
	require.JSONEq(t, `"ABC-412"`,
		string(mustJSONField(t, b, "handle")),
		"the wire shape is the spoken name, not the two columns")

	var got Task
	require.NoError(t, json.Unmarshal(b, &got), "a Go client must decode what the service wrote")
	require.Equal(t, task.Handle, got.Handle)
	require.Equal(t, task.BlockedBy, got.BlockedBy,
		"a handle inside a list is a handle and must round trip the same way")
	require.Equal(t, task.ID, got.ID)
}

// TestHandleJSONRejects pins the other half of the symmetry: a string that
// ParseHandle refuses must be refused on the wire too, with an error a caller
// can route to a 400 — not a zero Handle that downstream code would take for a
// task with no number.
func TestHandleJSONRejects(t *testing.T) {
	for _, raw := range []string{
		`"abc-412"`,                      // lower case: exact-match on the way in, as in ParseHandle
		`"ABC"`,                          // no number
		`"ABC-"`,                         // no digits
		`"412"`,                          // no key
		`"ABC-0"`,                        // numbering starts at 1
		`"ABC-x"`,                        // not a number
		`""`,                             // nothing at all
		`null`,                           // an absent handle is not a handle with no number
		`412`,                            // a bare number is not the shape MarshalJSON writes
		`{"project":"ABC","number":412}`, // the object form is not the wire shape
	} {
		var h Handle
		err := json.Unmarshal([]byte(raw), &h)
		require.ErrorIs(t, err, ErrInvalid, "%s should be refused with ErrInvalid", raw)
		require.Equal(t, Handle{}, h, "%s must not leave a value behind", raw)
	}
}

// mustJSONField pulls one field out of a JSON object for assertion; it is a
// test helper, not part of the package.
func mustJSONField(t *testing.T, obj []byte, field string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(obj, &m))
	raw, ok := m[field]
	require.True(t, ok, "field %q missing from %s", field, obj)
	return raw
}
