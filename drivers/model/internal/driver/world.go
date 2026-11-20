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
}

// Journal is what the driver needs of threads.
type Journal interface {
	// Threads is the threads the subject is a member of.
	Threads(ctx context.Context, subject uuid.UUID) ([]uuid.UUID, error)
	// Members is who is in the thread now.
	Members(ctx context.Context, thread uuid.UUID) ([]uuid.UUID, error)
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

func (t *HTTPThreads) Threads(ctx context.Context, subject uuid.UUID) ([]uuid.UUID, error) {
	var ts []struct {
		ID uuid.UUID `json:"id"`
	}
	if err := t.c.do(ctx, http.MethodGet, "/threads?member="+subject.String(), nil, &ts); err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	out := make([]uuid.UUID, 0, len(ts))
	for _, th := range ts {
		out = append(out, th.ID)
	}
	return out, nil
}

func (t *HTTPThreads) Members(ctx context.Context, thread uuid.UUID) ([]uuid.UUID, error) {
	var th struct {
		Members []uuid.UUID `json:"members"`
	}
	if err := t.c.do(ctx, http.MethodGet, "/threads/"+thread.String(), nil, &th); err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	return th.Members, nil
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
