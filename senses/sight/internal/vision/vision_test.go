package vision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescribeAsksChatWithTheFrame(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string            `json:"type"`
				Text     string            `json:"text"`
				ImageURL map[string]string `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"A stone door in torchlight.\nRead: KEEP OUT"}}]}`))
	}))
	defer srv.Close()

	res, err := Describe(context.Background(), Door{URL: srv.URL + "/v1/", Key: "k", Model: "vision"}, nil,
		[]byte{0xff, 0xd8, 0xff}, "image/jpeg", "en")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "A stone door in torchlight." || res.Read != "KEEP OUT" {
		t.Errorf("result %+v", res)
	}
	if auth != "Bearer k" || got.Model != "vision" || len(got.Messages) != 1 {
		t.Fatalf("request: auth %q model %q messages %d", auth, got.Model, len(got.Messages))
	}
	c := got.Messages[0].Content
	if len(c) != 2 || c[0].Type != "text" || !strings.Contains(c[0].Text, "code is en") ||
		c[1].Type != "image_url" || c[1].ImageURL["url"] != "data:image/jpeg;base64,/9j/" {
		t.Errorf("content %+v", c)
	}
}

func TestDescribeFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := Describe(context.Background(), Door{URL: srv.URL, Model: "vision"}, nil, []byte{1}, "image/png", ""); err == nil {
		t.Error("a 502 was not an error")
	}
}

func TestSplit(t *testing.T) {
	for answer, want := range map[string]Result{
		"A dark corridor.":                      {Text: "A dark corridor."},
		"A sign on a wall.\nRead: \"EXIT\"":     {Text: "A sign on a wall.", Read: "EXIT"},
		"An empty room.\n\nread: none.":         {Text: "An empty room."},
		"Two lines\nof scene.\nRead: Room 101 ": {Text: "Two lines of scene.", Read: "Room 101"},
		"":                                      {},
	} {
		if got := Split(answer); got != want {
			t.Errorf("Split(%q) = %+v, want %+v", answer, got, want)
		}
	}
}
