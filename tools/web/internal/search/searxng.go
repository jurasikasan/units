package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearXNG asks a SearXNG instance (GET /search?format=json), a metasearch
// engine run beside the tool: it keeps no index, it asks public engines
// (DuckDuckGo, Wikipedia, …) and merges what they answer. No key; its
// settings must allow the json format.
type SearXNG struct {
	URL    string // the instance's base, e.g. http://searxng:8080
	client *http.Client
}

func NewSearXNG(base string, timeout time.Duration) *SearXNG {
	return &SearXNG{URL: strings.TrimRight(base, "/"), client: &http.Client{Timeout: timeout}}
}

func (s *SearXNG) Search(ctx context.Context, query string, count int) ([]Result, error) {
	q := url.Values{"q": {query}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng did not answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("searxng's answer broke off: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// 403 is SearXNG's answer to format=json when its settings leave
		// json out.
		return nil, fmt.Errorf("searxng answered %d: %s", resp.StatusCode, short(raw))
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
		// [engine, why] per engine that failed this query.
		Unresponsive [][]string `json:"unresponsive_engines"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("searxng's answer is not JSON: %w", err)
	}
	if len(out.Results) == 0 && len(out.Unresponsive) > 0 {
		why := make([]string, 0, len(out.Unresponsive))
		for _, u := range out.Unresponsive {
			why = append(why, strings.Join(u, ": "))
		}
		return nil, fmt.Errorf("searxng found nothing, its engines failing: %s", strings.Join(why, "; "))
	}
	res := make([]Result, 0, min(count, len(out.Results)))
	for _, r := range out.Results {
		if len(res) == count {
			break
		}
		// An engine's link may come without a host (a redirect of its own,
		// http:///go/…): nothing a reader can open, so it is left out.
		if u, err := url.Parse(r.URL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		res = append(res, Result{Title: plain(r.Title), URL: r.URL, Snippet: plain(r.Content)})
	}
	return res, nil
}
