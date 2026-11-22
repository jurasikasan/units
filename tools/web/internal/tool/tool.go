// Package tool offers the web to whoever calls it, as two MCP tools: search
// (what a search engine finds for a query) and fetch (a page's words).
// What they return is the tool's: the engine's results, the page's text.
package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/tools/web/internal/fetch"
	"github.com/jurasikasan/units/tools/web/internal/search"
)

// DefaultCount is how many results a search returns when the call says not.
const DefaultCount = 5

// SearchIn is search's arguments.
type SearchIn struct {
	Query string `json:"query" jsonschema:"what to search the web for, in words"`
	Count int    `json:"count,omitempty" jsonschema:"how many results, 1 to 20; 5 when left out"`
}

// SearchOut is what search found.
type SearchOut struct {
	Query   string          `json:"query"`
	Results []search.Result `json:"results"`
}

// FetchIn is fetch's arguments.
type FetchIn struct {
	URL      string `json:"url" jsonschema:"the http or https url of the page to read"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"at most this many characters of the page's text; the tool's own most when left out or larger"`
}

// NewMCPServer offers search and fetch. With no searcher, search answers
// that it is not configured; fetch works all the same.
func NewMCPServer(s search.Searcher, f fetch.Fetcher, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "web", Version: version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search the web. Returns the results found, each a title, a url and a snippet. " +
			"count is how many, 1 to 20 (5 when left out). Read a result's page with fetch.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchIn) (*mcp.CallToolResult, SearchOut, error) {
		q := strings.TrimSpace(in.Query)
		if q == "" {
			return nil, SearchOut{}, errors.New("a query is needed: what to search for")
		}
		if s == nil {
			return nil, SearchOut{}, search.ErrNotConfigured
		}
		n := in.Count
		if n <= 0 {
			n = DefaultCount
		}
		n = min(n, search.MaxCount)
		res, err := s.Search(ctx, q, n)
		if err != nil {
			return nil, SearchOut{}, err
		}
		out := SearchOut{Query: q, Results: res}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: Found(out)}}}, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: "fetch",
		Description: "Read a web page: GET an http(s) url and return its text, HTML as markdown. " +
			"Only public addresses are reached. max_chars bounds the text returned.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FetchIn) (*mcp.CallToolResult, *fetch.Page, error) {
		if strings.TrimSpace(in.URL) == "" {
			return nil, nil, errors.New("a url is needed: the page to read")
		}
		p, err := f.Fetch(ctx, in.URL, in.MaxChars)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: Read(p)}}}, p, nil
	})
	return srv
}

// Found is search's results in words, as a model reads them.
func Found(out SearchOut) string {
	if len(out.Results) == 0 {
		return fmt.Sprintf("Nothing found for: %s", out.Query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results for: %s\n", len(out.Results), out.Query)
	for i, r := range out.Results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Read is a page in words: where it was read, its title, its text.
func Read(p *fetch.Page) string {
	var b strings.Builder
	if p.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", p.Title)
	}
	fmt.Fprintf(&b, "URL: %s (%s)\n\n%s", p.URL, p.ContentType, p.Text)
	if p.Truncated {
		b.WriteString("\n\n[the page goes on; cut here]")
	}
	return b.String()
}
