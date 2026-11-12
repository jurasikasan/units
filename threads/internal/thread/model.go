package thread

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Thread is a journal's header. Name and Members are read off its entries,
// never stored on it.
type Thread struct {
	ID        uuid.UUID  `json:"id"`
	Parent    *uuid.UUID `json:"parent,omitempty"`
	OpenedBy  *uuid.UUID `json:"opened_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	// Name is the name its last update entry gave it, empty if none did.
	Name string `json:"name"`
	// Members is filled on a single thread's read only.
	Members []uuid.UUID `json:"members,omitempty"`
}

// NewThread is what a caller sends to open a thread.
type NewThread struct {
	Parent   *uuid.UUID `json:"parent,omitempty"`
	OpenedBy *uuid.UUID `json:"opened_by,omitempty"`
}

// Kinds are ActivityStreams 2.0 activity types in lower case.
const (
	KindCreate = "create"
	KindJoin   = "join"
	KindLeave  = "leave"
	KindUpdate = "update"
)

// Entry is one thing that happened in a thread.
type Entry struct {
	ID      uuid.UUID `json:"id"`
	Thread  uuid.UUID `json:"thread"`
	Seq     int64     `json:"seq"`
	Sender  uuid.UUID `json:"sender"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	Content []Block   `json:"content"`
	Name    string    `json:"name,omitempty"`
}

// NewEntry is what a caller sends to append; the rest is the service's.
type NewEntry struct {
	Sender  uuid.UUID `json:"sender"`
	Kind    string    `json:"kind"`
	Content []Block   `json:"content,omitempty"`
	Name    string    `json:"name,omitempty"`
}

// Block types.
const (
	BlockText       = "text"
	BlockAudio      = "audio"
	BlockImage      = "image"
	BlockToolCall   = "tool_call"
	BlockToolResult = "tool_result"
)

// Block is one piece of an entry's content. One struct for every type: which
// fields a type may carry is checked on append (validate.go), so a field of
// another type is refused rather than kept and ignored.
type Block struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	URL        string          `json:"url,omitempty"`
	MediaType  string          `json:"media_type,omitempty"`
	SHA256     string          `json:"sha256,omitempty"`
	Transcript string          `json:"transcript,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty" swaggertype:"object"`
	CallID     string          `json:"call_id,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}
