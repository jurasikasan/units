package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Memory is the brain's thread in threads, spoken to over its REST API.
//
// The thread is Thread when configured. Otherwise it is found again by the
// brain's own subject (GET /threads?member=…, the newest), or opened and
// joined on the first start: a restart keeps its memory without an id
// carried by hand. Until a thread is had (threads down at start), every
// tick tries again; the brain thinks all the same.
type Memory struct {
	URL    string
	Client *http.Client
	Self   uuid.UUID
	Thread string // BRAIN_THREAD; empty: found or opened

	mu     sync.Mutex
	thread string
}

// Entry is a thread entry as the brain writes and reads it (threads CONTRACT
// v0.3.0): create entries with text and image blocks, and occurred.
type Entry struct {
	Sender   string    `json:"sender"`
	Kind     string    `json:"kind"`
	Content  []Block   `json:"content,omitempty"`
	Occurred *Occurred `json:"occurred,omitempty"`
}

// Block is a text, an image, a tool_call or a tool_result block (threads
// CONTRACT §1): a tool_call is id, name, args; a tool_result is call_id,
// text, is_error.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	URL       string          `json:"url,omitempty"`
	MediaType string          `json:"media_type,omitempty"`
	SHA256    string          `json:"sha256,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Occurred is perception time on an entry.
type Occurred struct {
	Start time.Time  `json:"start"`
	End   *time.Time `json:"end,omitempty"`
}

// Resolve gives the thread's id, finding or opening it the first time.
// opened is true when this call opened it.
func (m *Memory) Resolve(ctx context.Context) (id string, opened bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.thread != "" {
		return m.thread, false, nil
	}
	if m.Thread != "" {
		m.thread = m.Thread
		return m.thread, false, nil
	}
	var found []struct {
		ID string `json:"id"`
	}
	if err := m.call(ctx, http.MethodGet, "/threads?member="+url.QueryEscape(m.Self.String()), nil, &found); err != nil {
		return "", false, err
	}
	if len(found) > 0 { // newest first
		m.thread = found[0].ID
		return m.thread, false, nil
	}
	var t struct {
		ID string `json:"id"`
	}
	if err := m.call(ctx, http.MethodPost, "/threads", map[string]any{"opened_by": m.Self.String()}, &t); err != nil {
		return "", false, err
	}
	if err := m.call(ctx, http.MethodPost, "/threads/"+t.ID+"/entries",
		Entry{Sender: m.Self.String(), Kind: "join"}, nil); err != nil {
		return "", false, fmt.Errorf("thread %s opened, not joined: %w", t.ID, err)
	}
	m.thread = t.ID
	return m.thread, true, nil
}

// Recall reads the last n entries, oldest first.
func (m *Memory) Recall(ctx context.Context, n int) ([]Entry, error) {
	if n <= 0 {
		return nil, nil
	}
	id, _, err := m.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = m.call(ctx, http.MethodGet, "/threads/"+id+"/entries?tail=true&limit="+strconv.Itoa(n), nil, &out)
	return out, err
}

// Append writes one entry at the end of the thread.
func (m *Memory) Append(ctx context.Context, e Entry) error {
	id, _, err := m.Resolve(ctx)
	if err != nil {
		return err
	}
	return m.call(ctx, http.MethodPost, "/threads/"+id+"/entries", e, nil)
}

func (m *Memory) call(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.URL, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	raw, err := do(m.Client, req, 16<<20)
	if err != nil {
		return fmt.Errorf("threads %s %s: %w", method, strings.SplitN(path, "?", 2)[0], err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("threads %s %s: bad body: %w", method, path, err)
		}
	}
	return nil
}
