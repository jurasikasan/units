// Package artifacts puts files to artifact_store (its CONTRACT.md):
// PUT /v1/{space}/{name}, read back by anyone by GET /v1/_sha256/{hex}.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Store is where files go: artifact_store's base URL and the space.
type Store struct {
	URL    string
	Space  string
	Client *http.Client
}

// Ref is a file as put: what a reader needs to fetch it by content.
type Ref struct {
	SHA256      string
	Space       string
	Name        string
	ContentType string
}

// Put stores b under name in the store's space. The sha256 the store
// answers must be the one of b: a store that kept something else is an
// error, not a reference.
func (s Store) Put(ctx context.Context, name, contentType string, b []byte) (Ref, error) {
	u := strings.TrimRight(s.URL, "/") + "/v1/" + url.PathEscape(s.Space) + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(b))
	if err != nil {
		return Ref{}, err
	}
	req.Header.Set("Content-Type", contentType)
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Ref{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Ref{}, fmt.Errorf("PUT %s/%s: %d %s", s.Space, name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var got struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return Ref{}, fmt.Errorf("PUT %s/%s: answer: %w", s.Space, name, err)
	}
	sum := sha256.Sum256(b)
	want := hex.EncodeToString(sum[:])
	if got.SHA256 != want {
		return Ref{}, fmt.Errorf("PUT %s/%s: the store answered sha256 %q, the bytes are %s", s.Space, name, got.SHA256, want)
	}
	return Ref{SHA256: want, Space: s.Space, Name: name, ContentType: contentType}, nil
}
