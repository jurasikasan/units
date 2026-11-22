package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A SearXNG instance answering body with status, and what it was asked.
func searxng(t *testing.T, status int, body string) (*SearXNG, *http.Request) {
	t.Helper()
	asked := &http.Request{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = *r
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return NewSearXNG(s.URL+"/", 5*time.Second), asked
}

func TestSearXNGAsksAndReads(t *testing.T) {
	x, asked := searxng(t, 0, `{"query":"go lang","results":[
		{"title":"an engine's own redirect","url":"http:///go/v1/AeHz8on4","content":"no host"},
		{"title":"The Go Programming Language","url":"https://go.dev/","content":"Go is an open source language &amp; more.","engine":"duckduckgo"},
		{"title":"Go (game)","url":"https://en.wikipedia.org/wiki/Go_(game)","content":"An abstract  strategy board game.","engine":"wikipedia"},
		{"title":"third","url":"https://example.com/3","content":""}],
		"unresponsive_engines":[["brave","too many requests"]]}`)
	res, err := x.Search(context.Background(), "go lang", 2)
	if err != nil {
		t.Fatal(err)
	}
	if asked.URL.Path != "/search" || asked.URL.Query().Get("q") != "go lang" || asked.URL.Query().Get("format") != "json" {
		t.Fatalf("asked %s", asked.URL)
	}
	if len(res) != 2 {
		t.Fatalf("results %+v", res)
	}
	if res[0] != (Result{Title: "The Go Programming Language", URL: "https://go.dev/", Snippet: "Go is an open source language & more."}) {
		t.Fatalf("first %+v", res[0])
	}
	if res[1].Snippet != "An abstract strategy board game." {
		t.Fatalf("second %+v", res[1])
	}
}

// Nothing found while engines failed is the engines' failure, said; nothing
// found with every engine answering is an empty result, no error.
func TestSearXNGSaysWhenItsEnginesFailed(t *testing.T) {
	x, _ := searxng(t, 0, `{"results":[],"unresponsive_engines":[["duckduckgo","CAPTCHA"],["wikipedia","timeout"]]}`)
	_, err := x.Search(context.Background(), "x", 5)
	if err == nil || !strings.Contains(err.Error(), "duckduckgo: CAPTCHA; wikipedia: timeout") {
		t.Fatalf("err %v", err)
	}
	x, _ = searxng(t, 0, `{"results":[],"unresponsive_engines":[]}`)
	res, err := x.Search(context.Background(), "x", 5)
	if err != nil || len(res) != 0 {
		t.Fatalf("res %v err %v", res, err)
	}
}

func TestSearXNGWithoutJSONSaysSo(t *testing.T) {
	x, _ := searxng(t, http.StatusForbidden, `403 Forbidden`)
	_, err := x.Search(context.Background(), "x", 5)
	if err == nil || !strings.Contains(err.Error(), "searxng answered 403") {
		t.Fatalf("err %v", err)
	}
}
