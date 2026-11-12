package thread

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Cases are CONTRACT.md section 1 read as a table: what each kind carries and
// what each block type must and may have.

var alice = uuid.MustParse("00000000-0000-0000-0000-00000000a11c")

const digest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestValidateEntryKinds(t *testing.T) {
	text := []Block{{Type: BlockText, Text: "hi"}}
	cases := []struct {
		name string
		e    NewEntry
		ok   bool
	}{
		{"create with a block", NewEntry{Sender: alice, Kind: KindCreate, Content: text}, true},
		{"create with nothing", NewEntry{Sender: alice, Kind: KindCreate}, false},
		{"create with a name", NewEntry{Sender: alice, Kind: KindCreate, Content: text, Name: "x"}, false},
		{"join", NewEntry{Sender: alice, Kind: KindJoin}, true},
		{"join with content", NewEntry{Sender: alice, Kind: KindJoin, Content: text}, false},
		{"leave with a name", NewEntry{Sender: alice, Kind: KindLeave, Name: "x"}, false},
		{"update with a name", NewEntry{Sender: alice, Kind: KindUpdate, Name: "Kitchen"}, true},
		{"update without one", NewEntry{Sender: alice, Kind: KindUpdate}, false},
		{"update with content", NewEntry{Sender: alice, Kind: KindUpdate, Name: "x", Content: text}, false},
		{"no sender", NewEntry{Kind: KindJoin}, false},
		{"a kind AS2 has but we do not use", NewEntry{Sender: alice, Kind: "like"}, false},
		{"the old role-ish kind", NewEntry{Sender: alice, Kind: "message", Content: text}, false},
		{"capitalised AS2", NewEntry{Sender: alice, Kind: "Join"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEntry(tc.e)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInvalid)
			}
		})
	}
}

func TestValidateBlocks(t *testing.T) {
	cases := []struct {
		name string
		b    Block
		ok   bool
	}{
		{"text", Block{Type: BlockText, Text: "hi"}, true},
		{"empty text", Block{Type: BlockText}, false},
		{"text with a url", Block{Type: BlockText, Text: "hi", URL: "s3://x"}, false},
		{"audio", Block{Type: BlockAudio, URL: "store://bot/a.wav", MediaType: "audio/wav", SHA256: digest}, true},
		{"audio with transcript", Block{Type: BlockAudio, URL: "u", MediaType: "audio/wav", SHA256: digest, Transcript: "hello"}, true},
		{"audio without digest", Block{Type: BlockAudio, URL: "u", MediaType: "audio/wav"}, false},
		{"audio with upper-case digest", Block{Type: BlockAudio, URL: "u", MediaType: "audio/wav", SHA256: strings.ToUpper(digest)}, false},
		{"image", Block{Type: BlockImage, URL: "u", MediaType: "image/png", SHA256: digest}, true},
		{"image with transcript", Block{Type: BlockImage, URL: "u", MediaType: "image/png", SHA256: digest, Transcript: "x"}, false},
		{"tool_call", Block{Type: BlockToolCall, ID: "c1", Name: "look", Args: json.RawMessage(`{"q":1}`)}, true},
		{"tool_call with empty args", Block{Type: BlockToolCall, ID: "c1", Name: "look", Args: json.RawMessage(`{}`)}, true},
		{"tool_call without args", Block{Type: BlockToolCall, ID: "c1", Name: "look"}, false},
		{"tool_call with args as a string", Block{Type: BlockToolCall, ID: "c1", Name: "look", Args: json.RawMessage(`"{\"q\":1}"`)}, false},
		{"tool_call with null args", Block{Type: BlockToolCall, ID: "c1", Name: "look", Args: json.RawMessage(`null`)}, false},
		{"tool_result", Block{Type: BlockToolResult, CallID: "c1", Text: "42"}, true},
		{"tool_result, empty, failed", Block{Type: BlockToolResult, CallID: "c1", IsError: true}, true},
		{"tool_result without call_id", Block{Type: BlockToolResult, Text: "42"}, false},
		{"tool_result with a name", Block{Type: BlockToolResult, CallID: "c1", Name: "look"}, false},
		{"is_error on text", Block{Type: BlockText, Text: "x", IsError: true}, false},
		{"unknown type", Block{Type: "video", URL: "u"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBlock(tc.b)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInvalid)
			}
		})
	}
}
