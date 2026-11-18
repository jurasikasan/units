package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// lastContent runs body and returns the content of the last message the model
// was asked, as it came.
func lastContent(t *testing.T, body string) json.RawMessage {
	t.Helper()
	f := &fakeModel{pieces: []string{"ok"}}
	resp := post(t, context.Background(), serve(t, f).URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	if got := kinds(read(t, resp.Body)); !strings.HasSuffix(got, "RUN_FINISHED") {
		t.Fatalf("events %s", got)
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(f.raw[len(f.raw)-1], &m); err != nil {
		t.Fatal(err)
	}
	return m.Content
}

func TestAttachmentsGoAsContentParts(t *testing.T) {
	note := base64.StdEncoding.EncodeToString([]byte("the key is 42 <b>"))
	body := `{"messages":[{"role":"user","content":[
		{"type":"text","text":"what is here?"},
		{"type":"image","source":{"type":"data","value":"iVBORw0K","mimeType":"image/png"},"metadata":{"filename":"cat.png"}},
		{"type":"image","source":{"type":"url","value":"https://example.com/a.jpg"}},
		{"type":"document","source":{"type":"data","value":"JVBERi0x","mimeType":"application/pdf"},"metadata":{"filename":"a.pdf"}},
		{"type":"document","source":{"type":"data","value":"` + note + `","mimeType":"text/plain"},"metadata":{"filename":"n\"ote.txt"}},
		{"type":"audio","source":{"type":"data","value":"AAAA","mimeType":"audio/wav"}}]}]}`
	var got []map[string]any
	if err := json.Unmarshal(lastContent(t, body), &got); err != nil {
		t.Fatalf("content is not parts: %v", err)
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(got)
	b := strings.TrimSpace(buf.String())
	want := `[{"text":"what is here?","type":"text"},` +
		`{"image_url":{"url":"data:image/png;base64,iVBORw0K"},"type":"image_url"},` +
		`{"image_url":{"url":"https://example.com/a.jpg"},"type":"image_url"},` +
		`{"file":{"file_data":"data:application/pdf;base64,JVBERi0x","filename":"a.pdf"},"type":"file"},` +
		`{"text":"<attachment name=\"n&#34;ote.txt\">\nthe key is 42 <b>\n</attachment>","type":"text"}]`
	if string(b) != want {
		t.Errorf("parts\n got %s\nwant %s", b, want)
	}
}

func TestWordsOnlyStayAString(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"audio","source":{"type":"data","value":"AAAA","mimeType":"audio/wav"}}]}]}`
	if got := string(lastContent(t, body)); got != `"a\nb"` {
		t.Errorf("content %s", got)
	}
}

func TestAPictureAloneIsAMessage(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"data","value":"iVBORw0K","mimeType":"image/png"}}]}]}`
	if got := string(lastContent(t, body)); !strings.Contains(got, `"image_url"`) {
		t.Errorf("content %s", got)
	}
}

func TestATooLargeRunIsRefused(t *testing.T) {
	url := serve(t, &fakeModel{}).URL
	big := strings.Repeat("A", MaxBody)
	body := `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"data","value":"` + big + `","mimeType":"image/png"}}]}]}`
	resp := post(t, context.Background(), url, "alice", body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d", resp.StatusCode)
	}
}
