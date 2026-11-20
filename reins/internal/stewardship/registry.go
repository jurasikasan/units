package stewardship

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Registry is what reins needs of `subjects`.
type Registry interface {
	// Get is one subject; found is false when there is none or it is deleted.
	Get(ctx context.Context, id uuid.UUID) (s Subject, found bool, err error)
	// Create makes a subject: ErrNickTaken on 409, ErrNickRefused on 400.
	Create(ctx context.Context, n NewSubject) (Subject, error)
	// Raw is one subject as `subjects` sent it, every field, known to reins
	// or not; found is false when there is none or it is deleted.
	Raw(ctx context.Context, id uuid.UUID) (raw map[string]json.RawMessage, found bool, err error)
	// Replace writes a subject whole (PUT): ErrNoSubject on 404.
	Replace(ctx context.Context, id uuid.UUID, raw map[string]json.RawMessage) (Subject, error)
}

// HTTPRegistry speaks to `subjects` over its REST routes.
type HTTPRegistry struct {
	base   string
	client *http.Client
}

func NewHTTPRegistry(base string) *HTTPRegistry {
	return &HTTPRegistry{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *HTTPRegistry) Get(ctx context.Context, id uuid.UUID) (Subject, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/subjects/"+id.String(), nil)
	if err != nil {
		return Subject{}, false, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return Subject{}, false, fmt.Errorf("subjects: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var s Subject
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			return Subject{}, false, fmt.Errorf("subjects: decode subject: %w", err)
		}
		return s, true, nil
	case http.StatusNotFound:
		return Subject{}, false, nil
	default:
		return Subject{}, false, fmt.Errorf("subjects: GET /subjects/%s: %d %s", id, resp.StatusCode, short(resp.Body))
	}
}

func (r *HTTPRegistry) Create(ctx context.Context, n NewSubject) (Subject, error) {
	body, err := json.Marshal(n)
	if err != nil {
		return Subject{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/subjects", bytes.NewReader(body))
	if err != nil {
		return Subject{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return Subject{}, fmt.Errorf("subjects: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated:
		var s Subject
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			return Subject{}, fmt.Errorf("subjects: decode subject: %w", err)
		}
		return s, nil
	case http.StatusConflict:
		return Subject{}, ErrNickTaken
	case http.StatusBadRequest:
		return Subject{}, fmt.Errorf("%w: %s", ErrNickRefused, short(resp.Body))
	default:
		return Subject{}, fmt.Errorf("subjects: POST /subjects: %d %s", resp.StatusCode, short(resp.Body))
	}
}

func (r *HTTPRegistry) Raw(ctx context.Context, id uuid.UUID) (map[string]json.RawMessage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/subjects/"+id.String(), nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("subjects: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			return nil, false, fmt.Errorf("subjects: decode subject: %w", err)
		}
		return raw, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("subjects: GET /subjects/%s: %d %s", id, resp.StatusCode, short(resp.Body))
	}
}

func (r *HTTPRegistry) Replace(ctx context.Context, id uuid.UUID, raw map[string]json.RawMessage) (Subject, error) {
	body, err := json.Marshal(raw)
	if err != nil {
		return Subject{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.base+"/subjects/"+id.String(), bytes.NewReader(body))
	if err != nil {
		return Subject{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return Subject{}, fmt.Errorf("subjects: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var s Subject
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			return Subject{}, fmt.Errorf("subjects: decode subject: %w", err)
		}
		return s, nil
	case http.StatusNotFound:
		return Subject{}, ErrNoSubject
	default:
		return Subject{}, fmt.Errorf("subjects: PUT /subjects/%s: %d %s", id, resp.StatusCode, short(resp.Body))
	}
}

func short(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}
