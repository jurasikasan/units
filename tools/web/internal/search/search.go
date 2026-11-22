// Package search asks a web search engine and hands back what it found:
// a title, a url and a snippet per result. The engine is behind Searcher;
// Brave's Search API is the one there is.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Result is one thing found.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// Searcher is a web search engine.
type Searcher interface {
	Search(ctx context.Context, query string, count int) ([]Result, error)
}

// ErrNotConfigured is search's answer when no engine is set up.
var ErrNotConfigured = errors.New("search is not configured: the tool has no search engine key (BRAVE_API_KEY)")

// MaxCount is the most results one search returns (Brave's own most).
const MaxCount = 20

// BraveURL is Brave's web search endpoint.
const BraveURL = "https://api.search.brave.com/res/v1/web/search"

// Brave asks Brave's Search API (GET /res/v1/web/search, the key in
// X-Subscription-Token).
type Brave struct {
	URL    string
	Key    string
	client *http.Client
}

func NewBrave(key string, timeout time.Duration) *Brave {
	return &Brave{URL: BraveURL, Key: key, client: &http.Client{Timeout: timeout}}
}

func (b *Brave) Search(ctx context.Context, query string, count int) ([]Result, error) {
	q := url.Values{"q": {query}, "count": {strconv.Itoa(count)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.Key)
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave search did not answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("brave search's answer broke off: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave search answered %d: %s", resp.StatusCode, braveError(raw))
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("brave search's answer is not JSON: %w", err)
	}
	res := make([]Result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		res = append(res, Result{Title: plain(r.Title), URL: r.URL, Snippet: plain(r.Description)})
	}
	if len(res) > count {
		res = res[:count]
	}
	return res, nil
}

// braveError is Brave's error detail, else the body as it came, short.
func braveError(raw []byte) string {
	var e struct {
		Error struct {
			Detail string `json:"detail"`
			Code   string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Detail != "" {
		return e.Error.Detail
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

var tag = regexp.MustCompile(`<[^>]*>`)

// plain is a snippet without the engine's markup (<strong> on the words
// matched) and with its entities read.
func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tag.ReplaceAllString(s, ""))), " ")
}
