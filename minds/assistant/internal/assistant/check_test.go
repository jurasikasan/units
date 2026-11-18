package assistant

import (
	"context"
	"strings"
	"testing"
)

const picture = `{"type":"image","source":{"type":"data","value":"iVBORw0K","mimeType":"image/png"},"metadata":{"filename":"cat.png"}}`
const pdf = `{"type":"document","source":{"type":"data","value":"JVBERi0x","mimeType":"application/pdf"},"metadata":{"filename":"a.pdf"}}`

// deployment is one entry of LiteLLM's /v1/model/info.
func deployment(name, modelInfo string) string {
	return `{"model_name":"` + name + `","litellm_params":{"model":"x"},"model_info":` + modelInfo + `}`
}

func modelInfo(deployments ...string) string {
	return `{"data":[` + strings.Join(deployments, ",") + `]}`
}

// ask runs a conversation whose last message carries parts, on a model that
// LiteLLM describes with info; it returns the events and whether the model
// was asked.
func ask(t *testing.T, info string, parts ...string) ([]event, bool) {
	t.Helper()
	f := &fakeModel{pieces: []string{"ok"}, info: info}
	body := `{"runId":"r1","messages":[{"role":"user","content":[{"type":"text","text":"look"},` + strings.Join(parts, ",") + `]}]}`
	resp := post(t, context.Background(), serve(t, f).URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	return read(t, resp.Body), f.raw != nil
}

func TestAPictureToATextOnlyModelIsSaid(t *testing.T) {
	info := modelInfo(
		deployment("other", `{"supports_vision":true}`),
		deployment("m", `{"supports_vision":false,"supports_pdf_input":false}`),
	)
	evs, asked := ask(t, info, picture, pdf, picture)
	if got := kinds(evs); got != "RUN_STARTED RUN_ERROR" || asked {
		t.Fatalf("events %s, model asked: %v", got, asked)
	}
	e := evs[1]
	if e.Code != CodeNotAccepted || e.RunID != "r1" {
		t.Errorf("code %q, run %q", e.Code, e.RunID)
	}
	if want := "The model m does not take pictures (cat.png) or PDFs (a.pdf)."; e.Message != want {
		t.Errorf("message\n got %q\nwant %q", e.Message, want)
	}
}

func TestAGroupTakesWhatAllItsDeploymentsTake(t *testing.T) {
	for _, c := range []struct {
		name, a, b string
		refused    bool
	}{
		{"both take it", `{"supports_vision":true}`, `{"supports_vision":true}`, false},
		{"one does not", `{"supports_vision":true}`, `{"supports_vision":false}`, true},
		{"one not known: sent", `{"supports_vision":true}`, `{"supports_vision":null}`, false},
		{"not said at all: sent", `{}`, `{}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			evs, asked := ask(t, modelInfo(deployment("m", c.a), deployment("m", c.b)), picture)
			if refused := kinds(evs) == "RUN_STARTED RUN_ERROR"; refused != c.refused || asked == c.refused {
				t.Errorf("events %s, model asked: %v", kinds(evs), asked)
			}
		})
	}
}

func TestNothingKnownSendsAsBefore(t *testing.T) {
	for name, info := range map[string]string{"no endpoint": "", "another model": modelInfo(deployment("n", `{"supports_vision":false}`)), "not json": "<html>"} {
		t.Run(name, func(t *testing.T) {
			evs, asked := ask(t, info, picture)
			if got := kinds(evs); !strings.HasSuffix(got, "RUN_FINISHED") || !asked {
				t.Errorf("events %s, model asked: %v", got, asked)
			}
		})
	}
}

func TestWordsDoNotAskWhatTheModelTakes(t *testing.T) {
	// No /model/info is asked: the fake would answer it with this, and the
	// run would end with an error.
	f := &fakeModel{pieces: []string{"ok"}, info: modelInfo(deployment("m", `{"supports_vision":false}`))}
	resp := post(t, context.Background(), serve(t, f).URL, "alice", hello)
	defer func() { _ = resp.Body.Close() }()
	if got := kinds(read(t, resp.Body)); !strings.HasSuffix(got, "RUN_FINISHED") {
		t.Errorf("events %s", got)
	}
}

func TestAPictureEarlierInTheConversationCountsToo(t *testing.T) {
	f := &fakeModel{pieces: []string{"ok"}, info: modelInfo(deployment("m", `{"supports_vision":false}`))}
	body := `{"messages":[{"role":"user","content":[` + picture + `]},{"role":"assistant","content":"a cat"},{"role":"user","content":"and now?"}]}`
	resp := post(t, context.Background(), serve(t, f).URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	evs := read(t, resp.Body)
	if got := kinds(evs); got != "RUN_STARTED RUN_ERROR" || !strings.Contains(evs[1].Message, "cat.png") {
		t.Errorf("events %s %q", got, evs)
	}
}

func TestWhatNothingCarriesIsSaid(t *testing.T) {
	for name, c := range map[string]struct{ part, says string }{
		"a video":         {`{"type":"video","source":{"type":"data","value":"AAAA","mimeType":"video/mp4"},"metadata":{"filename":"clip.mp4"}}`, "clip.mp4: a video"},
		"a sound by link": {`{"type":"audio","source":{"type":"url","value":"https://example.com/a.wav","mimeType":"audio/wav"}}`, "audio/wav: a sound goes"},
		"an ogg sound":    {`{"type":"audio","source":{"type":"data","value":"AAAA","mimeType":"audio/ogg"},"metadata":{"filename":"v.ogg"}}`, "v.ogg: a model takes a sound as WAV or MP3"},
		"a PDF by link":   {`{"type":"document","source":{"type":"url","value":"https://example.com/a.pdf","mimeType":"application/pdf"}}`, "application/pdf: a document goes"},
	} {
		t.Run(name, func(t *testing.T) {
			evs, asked := ask(t, "", c.part)
			if got := kinds(evs); got != "RUN_STARTED RUN_ERROR" || asked {
				t.Fatalf("events %s, model asked: %v", got, asked)
			}
			if evs[1].Code != CodeNotCarried || !strings.Contains(evs[1].Message, c.says) {
				t.Errorf("code %q, message %q", evs[1].Code, evs[1].Message)
			}
		})
	}
}

func TestASoundAloneIsAMessage(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"audio","source":{"type":"data","value":"AAAA","mimeType":"audio/mpeg"}}]}]}`
	if got := string(lastContent(t, body)); !strings.Contains(got, `"format":"mp3"`) {
		t.Errorf("content %s", got)
	}
}
