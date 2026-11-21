package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Entry is an entry as threads keeps it (threads/CONTRACT.md, section 1).
type Entry struct {
	ID      uuid.UUID `json:"id"`
	Thread  uuid.UUID `json:"thread"`
	Seq     int64     `json:"seq"`
	Sender  uuid.UUID `json:"sender"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	Content []Block   `json:"content"`
}

// Block is one piece of an entry's content; the fields of every type.
type Block struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	URL        string `json:"url,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Name       string `json:"name,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	// tool_call
	ID   string          `json:"id,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	// tool_result: what the tool returned, its words and its media
	CallID  string  `json:"call_id,omitempty"`
	IsError bool    `json:"is_error,omitempty"`
	Media   []Block `json:"media,omitempty"`
}

// ThreadInfo is a thread as the driver reads it: who opened it, who is in
// it now.
type ThreadInfo struct {
	ID       uuid.UUID   `json:"id"`
	OpenedBy *uuid.UUID  `json:"opened_by,omitempty"`
	Members  []uuid.UUID `json:"members"`
}

// Journal is what the driver needs of threads.
type Journal interface {
	// Threads is the threads the subject is a member of, without members.
	Threads(ctx context.Context, subject uuid.UUID) ([]ThreadInfo, error)
	// Thread is one thread: who opened it, who is in it now.
	Thread(ctx context.Context, thread uuid.UUID) (ThreadInfo, error)
	// Tail is the last n entries of the thread, oldest first.
	Tail(ctx context.Context, thread uuid.UUID, n int) ([]Entry, error)
	// Say appends a create entry sent by the subject; the entry as stored.
	Say(ctx context.Context, thread, sender uuid.UUID, content []Block) (Entry, error)
}

// Registry is what the driver needs of subjects.
type Registry interface {
	// Find is the subject with this nick or id.
	Find(ctx context.Context, nickOrID string) (Subject, error)
	// Name is what a subject is called in a prompt: its name, else its nick.
	Name(ctx context.Context, id uuid.UUID) string
}

// Subject is a subject as subjects keeps it, the fields the driver reads.
type Subject struct {
	ID   uuid.UUID `json:"id"`
	Nick string    `json:"nick"`
	Name string    `json:"name"`
}

type client struct {
	base string
	http *http.Client
}

func (c client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// HTTPThreads speaks to threads over its REST routes.
type HTTPThreads struct{ c client }

func NewHTTPThreads(base string) *HTTPThreads {
	return &HTTPThreads{client{strings.TrimRight(base, "/"), &http.Client{Timeout: 30 * time.Second}}}
}

func (t *HTTPThreads) Threads(ctx context.Context, subject uuid.UUID) ([]ThreadInfo, error) {
	var ts []ThreadInfo
	if err := t.c.do(ctx, http.MethodGet, "/threads?member="+subject.String(), nil, &ts); err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	return ts, nil
}

func (t *HTTPThreads) Thread(ctx context.Context, thread uuid.UUID) (ThreadInfo, error) {
	var th ThreadInfo
	if err := t.c.do(ctx, http.MethodGet, "/threads/"+thread.String(), nil, &th); err != nil {
		return ThreadInfo{}, fmt.Errorf("threads: %w", err)
	}
	return th, nil
}

func (t *HTTPThreads) Tail(ctx context.Context, thread uuid.UUID, n int) ([]Entry, error) {
	var es []Entry
	q := url.Values{"tail": {"true"}, "limit": {fmt.Sprint(n)}}
	if err := t.c.do(ctx, http.MethodGet, "/threads/"+thread.String()+"/entries?"+q.Encode(), nil, &es); err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	return es, nil
}

func (t *HTTPThreads) Say(ctx context.Context, thread, sender uuid.UUID, content []Block) (Entry, error) {
	var e Entry
	body := map[string]any{"sender": sender, "kind": "create", "content": content}
	if err := t.c.do(ctx, http.MethodPost, "/threads/"+thread.String()+"/entries", body, &e); err != nil {
		return Entry{}, fmt.Errorf("threads: %w", err)
	}
	return e, nil
}

// HTTPSubjects speaks to subjects over its REST routes; names are kept a
// minute.
type HTTPSubjects struct {
	c     client
	mu    sync.Mutex
	names map[uuid.UUID]named
}

type named struct {
	name string
	at   time.Time
}

func NewHTTPSubjects(base string) *HTTPSubjects {
	return &HTTPSubjects{c: client{strings.TrimRight(base, "/"), &http.Client{Timeout: 10 * time.Second}}, names: map[uuid.UUID]named{}}
}

func (s *HTTPSubjects) Find(ctx context.Context, nickOrID string) (Subject, error) {
	if id, err := uuid.Parse(nickOrID); err == nil {
		var sub Subject
		if err := s.c.do(ctx, http.MethodGet, "/subjects/"+id.String(), nil, &sub); err != nil {
			return Subject{}, fmt.Errorf("subjects: %w", err)
		}
		return sub, nil
	}
	var all []Subject
	if err := s.c.do(ctx, http.MethodGet, "/subjects", nil, &all); err != nil {
		return Subject{}, fmt.Errorf("subjects: %w", err)
	}
	for _, sub := range all {
		if sub.Nick == nickOrID {
			return sub, nil
		}
	}
	return Subject{}, fmt.Errorf("subjects: no subject %q", nickOrID)
}

func (s *HTTPSubjects) Name(ctx context.Context, id uuid.UUID) string {
	s.mu.Lock()
	n, ok := s.names[id]
	s.mu.Unlock()
	if ok && time.Since(n.at) < time.Minute {
		return n.name
	}
	var sub Subject
	if err := s.c.do(ctx, http.MethodGet, "/subjects/"+id.String(), nil, &sub); err != nil {
		return id.String()[:8]
	}
	name := sub.Name
	if name == "" {
		name = sub.Nick
	}
	s.mu.Lock()
	s.names[id] = named{name, time.Now()}
	s.mu.Unlock()
	return name
}

// Reins is what the driver needs of reins: which subjects the brains
// hold, and a way to let go of its own hold.
type Reins interface {
	BrainHolds(ctx context.Context) ([]BrainHold, error)
	// Release lets go of the subject while brain:<profile> still holds
	// it; false when someone else (or nobody) does.
	Release(ctx context.Context, subject uuid.UUID, profile string) (bool, error)
}

// BrainHold is one subject a brain holds, as reins says it.
type BrainHold struct {
	Subject uuid.UUID `json:"subject"`
	Profile string    `json:"profile"`
}

// HTTPReins speaks to reins' inner port: GET /holds, POST
// /holds/{id}/release. Never through the door.
type HTTPReins struct{ c client }

func NewHTTPReins(base string) *HTTPReins {
	return &HTTPReins{client{strings.TrimRight(base, "/"), &http.Client{Timeout: 10 * time.Second}}}
}

func (r *HTTPReins) BrainHolds(ctx context.Context) ([]BrainHold, error) {
	var hs []BrainHold
	if err := r.c.do(ctx, http.MethodGet, "/holds", nil, &hs); err != nil {
		return nil, fmt.Errorf("reins: %w", err)
	}
	return hs, nil
}

func (r *HTTPReins) Release(ctx context.Context, subject uuid.UUID, profile string) (bool, error) {
	var out struct {
		Released bool `json:"released"`
	}
	if err := r.c.do(ctx, http.MethodPost, "/holds/"+subject.String()+"/release", map[string]string{"profile": profile}, &out); err != nil {
		return false, fmt.Errorf("reins: %w", err)
	}
	return out.Released, nil
}
