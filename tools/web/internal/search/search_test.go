package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A Brave endpoint: it answers from what it was asked, or refuses.
func brave(t *testing.T, status int) (*Brave, *http.Request) {
	t.Helper()
	asked := &http.Request{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = *r
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"ErrorResponse","error":{"code":"RATE_LIMITED","detail":"Request rate limit exceeded for plan.","status":429}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"search","query":{"original":"go"},"web":{"type":"search","results":[
			{"title":"The <strong>Go</strong> Programming Language","url":"https://go.dev/","description":"<strong>Go</strong> is an open source language &amp; more."},
			{"title":"Go (game)","url":"https://en.wikipedia.org/wiki/Go_(game)","description":"An abstract strategy board game."},
			{"title":"third","url":"https://example.com/3","description":""}]}}`))
	}))
	t.Cleanup(s.Close)
	b := NewBrave("secret-key", 5*time.Second)
	b.URL = s.URL + "/res/v1/web/search"
	return b, asked
}

func TestBraveAsksAndReads(t *testing.T) {
	b, asked := brave(t, 0)
	res, err := b.Search(context.Background(), "go lang", 2)
	if err != nil {
		t.Fatal(err)
	}
	if asked.URL.Path != "/res/v1/web/search" || asked.URL.Query().Get("q") != "go lang" || asked.URL.Query().Get("count") != "2" {
		t.Fatalf("asked %s", asked.URL)
	}
	if asked.Header.Get("X-Subscription-Token") != "secret-key" || asked.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers %v", asked.Header)
	}
	if len(res) != 2 {
		t.Fatalf("results %+v", res)
	}
	if res[0] != (Result{Title: "The Go Programming Language", URL: "https://go.dev/", Snippet: "Go is an open source language & more."}) {
		t.Fatalf("first %+v", res[0])
	}
}

func TestBraveSaysWhyItCouldNot(t *testing.T) {
	b, _ := brave(t, 429)
	_, err := b.Search(context.Background(), "x", 5)
	if err == nil || !strings.Contains(err.Error(), "429: Request rate limit exceeded") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("the key is in the error")
	}
}
