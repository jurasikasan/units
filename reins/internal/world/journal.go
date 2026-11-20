// Package world is reins as the face's door into the world: a principal
// writes into `threads` only as an avatar it steers, and reads the threads
// its avatars are in, as a list, page by page, and as one stream that
// follows them (stream.go).
//
// What happened is `threads`' (the journal); who may act as whom is
// stewardship's. Nothing is kept here.
package world

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Thread is a thread as `threads` reads it: its name from its last update
// entry, its members from their joins and leaves.
type Thread struct {
	ID        uuid.UUID   `json:"id"`
	Parent    *uuid.UUID  `json:"parent,omitempty"`
	OpenedBy  *uuid.UUID  `json:"opened_by,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	Name      string      `json:"name"`
	Members   []uuid.UUID `json:"members"`
}

// Entry is the part of an entry reins reads; the rest goes on as it came.
type Entry struct {
	Seq    int64     `json:"seq"`
	Sender uuid.UUID `json:"sender"`
	Kind   string    `json:"kind"`
}

// Journal is what reins needs of `threads`.
type Journal interface {
	// Member is the threads where the subject is a member, newest first,
	// without their members.
	Member(ctx context.Context, subject uuid.UUID) ([]Thread, error)
	// Get is one thread with its members; ErrNoThread when there is none.
	Get(ctx context.Context, id uuid.UUID) (Thread, error)
	// Open mints a thread opened by the subject.
	Open(ctx context.Context, by uuid.UUID) (Thread, error)
	// Append writes one entry; the body is threads' NewEntry. A refusal of
	// its shape is *Refused with threads' own words.
	Append(ctx context.Context, thread uuid.UUID, entry any) (json.RawMessage, error)
	// Entries is a page of a thread's entries, query as threads takes it.
	// A refusal of the query is *Refused.
	Entries(ctx context.Context, thread uuid.UUID, query url.Values) (json.RawMessage, error)
}

var ErrNoThread = errors.New("no such thread")

// Refused is threads saying no to what was asked (400), in its words.
type Refused struct{ Reason string }

func (r *Refused) Error() string { return r.Reason }

// HTTPJournal speaks to `threads` over its REST routes.
type HTTPJournal struct {
	base   string
	client *http.Client
}

func NewHTTPJournal(base string) *HTTPJournal {
	return &HTTPJournal{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 30 * time.Second}}
}

func (j *HTTPJournal) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, j.base+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tp := traceparent(ctx); tp != "" {
		req.Header.Set("traceparent", tp)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	return resp, nil
}

// read answers the body of a 2xx as it came; a 400 is *Refused, a 404
// ErrNoThread, anything else an error naming the call.
func read(resp *http.Response, call string) (json.RawMessage, error) {
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("threads: %s: %w", call, err)
		}
		return b, nil
	case resp.StatusCode == http.StatusBadRequest:
		return nil, &Refused{Reason: short(resp.Body)}
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoThread
	default:
		return nil, fmt.Errorf("threads: %s: %d %s", call, resp.StatusCode, short(resp.Body))
	}
}

func (j *HTTPJournal) Member(ctx context.Context, subject uuid.UUID) ([]Thread, error) {
	resp, err := j.do(ctx, http.MethodGet, "/threads?member="+subject.String(), nil)
	if err != nil {
		return nil, err
	}
	b, err := read(resp, "GET /threads")
	if err != nil {
		return nil, err
	}
	var ts []Thread
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("threads: decode threads: %w", err)
	}
	return ts, nil
}

func (j *HTTPJournal) Get(ctx context.Context, id uuid.UUID) (Thread, error) {
	resp, err := j.do(ctx, http.MethodGet, "/threads/"+id.String(), nil)
	if err != nil {
		return Thread{}, err
	}
	b, err := read(resp, "GET /threads/{id}")
	if err != nil {
		return Thread{}, err
	}
	var t Thread
	if err := json.Unmarshal(b, &t); err != nil {
		return Thread{}, fmt.Errorf("threads: decode thread: %w", err)
	}
	return t, nil
}

func (j *HTTPJournal) Open(ctx context.Context, by uuid.UUID) (Thread, error) {
	resp, err := j.do(ctx, http.MethodPost, "/threads", map[string]any{"opened_by": by})
	if err != nil {
		return Thread{}, err
	}
	b, err := read(resp, "POST /threads")
	if err != nil {
		return Thread{}, err
	}
	var t Thread
	if err := json.Unmarshal(b, &t); err != nil {
		return Thread{}, fmt.Errorf("threads: decode thread: %w", err)
	}
	return t, nil
}

func (j *HTTPJournal) Append(ctx context.Context, thread uuid.UUID, entry any) (json.RawMessage, error) {
	resp, err := j.do(ctx, http.MethodPost, "/threads/"+thread.String()+"/entries", entry)
	if err != nil {
		return nil, err
	}
	return read(resp, "POST /threads/{id}/entries")
}

func (j *HTTPJournal) Entries(ctx context.Context, thread uuid.UUID, query url.Values) (json.RawMessage, error) {
	path := "/threads/" + thread.String() + "/entries"
	if q := query.Encode(); q != "" {
		path += "?" + q
	}
	resp, err := j.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return read(resp, "GET /threads/{id}/entries")
}

func short(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

type traceKey struct{}

// WithTraceparent carries a request's W3C traceparent on to threads, so an
// append's event is in the trace of the request that caused it.
func WithTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, tp)
}

func traceparent(ctx context.Context) string {
	s, _ := ctx.Value(traceKey{}).(string)
	return s
}
