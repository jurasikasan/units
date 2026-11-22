package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/tools/web/internal/fetch"
	"github.com/jurasikasan/units/tools/web/internal/search"
)

// fakeSearch is a search engine that finds count results for anything.
type fakeSearch struct{ asked []any }

func (f *fakeSearch) Search(_ context.Context, q string, count int) ([]search.Result, error) {
	f.asked = append(f.asked, q, count)
	var out []search.Result
	for i := range min(count, 3) {
		out = append(out, search.Result{Title: q + " " + string(rune('a'+i)), URL: "https://example.com/" + string(rune('a'+i)), Snippet: "about " + q})
	}
	return out, nil
}

// fakeFetch reads any url as one line, and refuses private ones.
type fakeFetch struct{}

func (fakeFetch) Fetch(_ context.Context, u string, _ int) (*fetch.Page, error) {
	if strings.Contains(u, "169.254") {
		return nil, &fetch.Refused{Why: "169.254.169.254 is not a public address"}
	}
	return &fetch.Page{URL: u, Status: 200, ContentType: "text/html", Title: "T", Text: "# Hello", Truncated: true}, nil
}

func connect(t *testing.T, s search.Searcher) *mcp.ClientSession {
	t.Helper()
	srv := NewMCPServer(s, fakeFetch{}, "test")
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res, res.Content[0].(*mcp.TextContent).Text
}

func TestTheTools(t *testing.T) {
	cs := connect(t, &fakeSearch{})
	var names []string
	for tl, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tl.Name)
		schema, _ := json.Marshal(tl.InputSchema)
		want := map[string]string{"search": `"required":["query"]`, "fetch": `"required":["url"]`}[tl.Name]
		if !strings.Contains(string(schema), want) {
			t.Fatalf("%s schema %s", tl.Name, schema)
		}
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "fetch,search" {
		t.Fatalf("tools %v", names)
	}
}

func TestSearchOverMCP(t *testing.T) {
	f := &fakeSearch{}
	cs := connect(t, f)
	res, text := call(t, cs, "search", map[string]any{"query": " go "})
	if res.IsError || !strings.HasPrefix(text, "3 results for: go\n\n1. go a\n   https://example.com/a\n   about go") {
		t.Fatalf("%v %q", res.IsError, text)
	}
	if f.asked[1] != DefaultCount {
		t.Fatalf("asked %v", f.asked)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"query":"go"`) || !strings.Contains(string(b), `"url":"https://example.com/b"`) {
		t.Fatalf("structured %s", b)
	}
	call(t, cs, "search", map[string]any{"query": "x", "count": 50})
	if f.asked[3] != search.MaxCount {
		t.Fatalf("asked %v", f.asked)
	}
	if res, text := call(t, cs, "search", map[string]any{"query": " "}); !res.IsError || !strings.Contains(text, "a query is needed") {
		t.Fatalf("%v %q", res.IsError, text)
	}
}

// With no engine, search says so as the tool's error, and fetch still works.
func TestSearchNotConfigured(t *testing.T) {
	cs := connect(t, nil)
	res, text := call(t, cs, "search", map[string]any{"query": "go"})
	if !res.IsError || !strings.Contains(text, "search is not configured") {
		t.Fatalf("%v %q", res.IsError, text)
	}
	if res, _ := call(t, cs, "fetch", map[string]any{"url": "https://example.com/"}); res.IsError {
		t.Fatal("fetch did not work without search")
	}
}

func TestFetchOverMCP(t *testing.T) {
	cs := connect(t, nil)
	res, text := call(t, cs, "fetch", map[string]any{"url": "https://example.com/"})
	if res.IsError || text != "Title: T\nURL: https://example.com/ (text/html)\n\n# Hello\n\n[the page goes on; cut here]" {
		t.Fatalf("%v %q", res.IsError, text)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"text":"# Hello"`) || !strings.Contains(string(b), `"truncated":true`) {
		t.Fatalf("structured %s", b)
	}
	res, text = call(t, cs, "fetch", map[string]any{"url": "http://169.254.169.254/latest/meta-data/"})
	if !res.IsError || !strings.Contains(text, "not a public address") {
		t.Fatalf("%v %q", res.IsError, text)
	}
}
