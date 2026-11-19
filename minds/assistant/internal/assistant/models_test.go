package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The fake's GET /v1/models, LiteLLM's: every group the key may use.
const groups = `{"object":"list","data":[{"id":"m"},{"id":"other"},{"id":"stt"},{"id":"realtime"}]}`

// With /v1/model/info: stt and realtime are not for chat; m and other say
// no mode at all, as a model LiteLLM does not know.
var modes = modelInfo(
	deployment("m", `{}`),
	deployment("other", `{"mode":"chat"}`),
	deployment("stt", `{"mode":"audio_transcription"}`),
	deployment("realtime", `{"mode":"realtime"}`),
)

func offeredBy(t *testing.T, f *fakeModel) []string {
	t.Helper()
	model := httptest.NewServer(f)
	t.Cleanup(model.Close)
	mind := httptest.NewServer(http.HandlerFunc(New(model.URL, "m", "k", "").Models))
	t.Cleanup(mind.Close)
	req, _ := http.NewRequest(http.MethodGet, mind.URL, nil)
	req.Header.Set("X-Principal-Id", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID, Object string
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || list.Object != "list" {
		t.Fatalf("not OpenAI's list: %v %q", err, list.Object)
	}
	var ids []string
	for _, m := range list.Data {
		if m.Object != "model" {
			t.Errorf("%s: object %q", m.ID, m.Object)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func TestTheGroupsForChatAreOfferedTheDefaultFirst(t *testing.T) {
	for _, c := range []struct {
		name string
		f    *fakeModel
		want string
	}{
		{"modes known", &fakeModel{models: `{"data":[{"id":"other"},{"id":"m"},{"id":"stt"},{"id":"realtime"}]}`, info: modes}, "m other"},
		{"modes not known: all", &fakeModel{models: groups}, "m other stt realtime"},
		{"no list: the default alone", &fakeModel{info: modes}, "m"},
	} {
		if got := strings.Join(offeredBy(t, c.f), " "); got != c.want {
			t.Errorf("%s: offered %q, want %q", c.name, got, c.want)
		}
	}
}

// pick runs a conversation with forwardedProps and returns the events and
// the model the fake was asked as.
func pick(t *testing.T, forwarded string) ([]event, string) {
	t.Helper()
	f := &fakeModel{pieces: []string{"ok"}, models: groups, info: modes}
	body := `{"runId":"r1","messages":[{"role":"user","content":"hi"}]` + forwarded + `}`
	resp := post(t, context.Background(), serve(t, f).URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	return read(t, resp.Body), f.model
}

func TestAPickIsTheModelAsked(t *testing.T) {
	evs, asked := pick(t, `,"forwardedProps":{"modelName":"other","reasoningEffort":"low"}`)
	if kinds(evs) != "RUN_STARTED TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED" || asked != "other" {
		t.Fatalf("events %s, asked as %q", kinds(evs), asked)
	}
}

func TestNoPickKeepsTheDefault(t *testing.T) {
	for _, fw := range []string{``, `,"forwardedProps":{}`, `,"forwardedProps":{"modelName":""}`, `,"forwardedProps":"m"`} {
		if _, asked := pick(t, fw); asked != "m" {
			t.Errorf("%s: asked as %q, want the default m", fw, asked)
		}
	}
}

func TestAPickNotOfferedIsSaid(t *testing.T) {
	for _, name := range []string{"nope", "stt"} {
		evs, asked := pick(t, `,"forwardedProps":{"modelName":"`+name+`"}`)
		if kinds(evs) != "RUN_STARTED RUN_ERROR" || asked != "" {
			t.Fatalf("%s: events %s, asked as %q", name, kinds(evs), asked)
		}
		if e := evs[1]; e.Code != CodeNotOffered || e.Message != "The model "+name+" is not one this assistant offers." {
			t.Errorf("%s: %q %q", name, e.Code, e.Message)
		}
	}
}

func TestTheListNeedsAPrincipal(t *testing.T) {
	rec := httptest.NewRecorder()
	New("http://nowhere", "m", "", "").Models(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if b, _ := io.ReadAll(rec.Body); rec.Code != http.StatusUnauthorized {
		t.Errorf("%d %s", rec.Code, b)
	}
}
