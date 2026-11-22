package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The fetcher: the same Client in a process of its own, on a network that
// can only go out (the compose files put it there), asked over HTTP by the
// MCP server, which stays on the stack's network. What reaches into the
// stack (the MCP server) does not fetch; what fetches cannot reach into
// the stack.
//
//	POST /fetch {"url", "max_chars"?} → 200 Page | 422 {"error"} (refused or failed)

type fetchIn struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

type fetchErr struct {
	Error   string `json:"error"`
	Refused bool   `json:"refused,omitempty"`
}

// Handler serves POST /fetch with f.
func Handler(f Fetcher) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var in fetchIn
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(fetchErr{Error: "the body is no {url, max_chars}"})
			return
		}
		p, err := f.Fetch(r.Context(), in.URL, in.MaxChars)
		if err != nil {
			var ref *Refused
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(fetchErr{Error: err.Error(), Refused: errors.As(err, &ref)})
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	})
	return mux
}

// Remote asks a fetcher over HTTP.
type Remote struct {
	url    string
	client *http.Client
}

// NewRemote is a Fetcher that asks the fetcher at base (http://web-fetcher:8080).
// timeout bounds a whole call, the fetcher's own fetch inside it.
func NewRemote(base string, timeout time.Duration) *Remote {
	return &Remote{url: strings.TrimSuffix(base, "/") + "/fetch", client: &http.Client{Timeout: timeout}}
}

func (r *Remote) Fetch(ctx context.Context, rawURL string, maxChars int) (*Page, error) {
	b, _ := json.Marshal(fetchIn{URL: rawURL, MaxChars: maxChars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the fetcher did not answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("the fetcher's answer broke off: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e fetchErr
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			return nil, fmt.Errorf("the fetcher answered %s", resp.Status)
		}
		if e.Refused {
			return nil, &Refused{e.Error}
		}
		return nil, errors.New(e.Error)
	}
	var p Page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("the fetcher's answer is no page: %w", err)
	}
	return &p, nil
}
