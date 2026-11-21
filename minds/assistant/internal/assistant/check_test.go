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
// LiteLLM describes with info; it returns the events and the messages the
// model was asked, as they came, all in one string ("" when not asked).
func ask(t *testing.T, info string, parts ...string) ([]event, string) {
	t.Helper()
	body := `{"runId":"r1","messages":[{"role":"user","content":[{"type":"text","text":"look"},` + strings.Join(parts, ",") + `]}]}`
	return run(t, info, body)
}

func run(t *testing.T, info, body string) ([]event, string) {
	t.Helper()
	f := &fakeModel{pieces: []string{"ok"}, info: info}
	resp := post(t, context.Background(), serve(t, f).URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	evs := read(t, resp.Body)
	var asked []string
	for _, m := range f.raw {
		asked = append(asked, string(m))
	}
	return evs, strings.Join(asked, "\n")
}

// wentOn fails the test unless the run went on to its end.
func wentOn(t *testing.T, evs []event) {
	t.Helper()
	got := kinds(evs)
	if strings.Contains(got, "RUN_ERROR") || !strings.HasSuffix(got, "RUN_FINISHED") {
		t.Fatalf("events %s", got)
	}
}

func TestAPictureToATextOnlyModelGoesAsWords(t *testing.T) {
	log := logs(t)
	info := modelInfo(
		deployment("other", `{"supports_vision":true}`),
		deployment("m", `{"supports_vision":false,"supports_pdf_input":false}`),
	)
	evs, asked := ask(t, info, picture, pdf)
	wentOn(t, evs)
	want := `"content":"look\n[an image is attached here, not shown to you: cat.png]\n[a PDF is attached here, not shown to you: a.pdf]"`
	if !strings.Contains(asked, want) {
		t.Errorf("asked\n %s\nwant in it\n %s", asked, want)
	}
	for _, w := range []string{"level=WARN", "model=m", "kind=image", "name=cat.png", "kind=pdf", "name=a.pdf"} {
		if !strings.Contains(log.String(), w) {
			t.Errorf("log has no %q:\n%s", w, log)
		}
	}
}

func TestOnlyWhatTheModelDoesNotTakeIsReplaced(t *testing.T) {
	evs, asked := ask(t, modelInfo(deployment("m", `{"supports_vision":false,"supports_pdf_input":true}`)), picture, pdf)
	wentOn(t, evs)
	if !strings.Contains(asked, "not shown to you: cat.png") || !strings.Contains(asked, `"file_data":"data:application/pdf;base64,JVBERi0x"`) {
		t.Errorf("asked %s", asked)
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
			wentOn(t, evs)
			if refused := strings.Contains(asked, "not shown to you: cat.png"); refused != c.refused || strings.Contains(asked, "image_url") == c.refused {
				t.Errorf("asked %s", asked)
			}
		})
	}
}

func TestNothingKnownSendsAsBefore(t *testing.T) {
	for name, info := range map[string]string{"no endpoint": "", "another model": modelInfo(deployment("n", `{"supports_vision":false}`)), "not json": "<html>"} {
		t.Run(name, func(t *testing.T) {
			evs, asked := ask(t, info, picture)
			wentOn(t, evs)
			if !strings.Contains(asked, "image_url") {
				t.Errorf("asked %s", asked)
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
	body := `{"messages":[{"role":"user","content":[` + picture + `]},{"role":"assistant","content":"a cat"},{"role":"user","content":"and now?"}]}`
	evs, asked := run(t, modelInfo(deployment("m", `{"supports_vision":false}`)), body)
	wentOn(t, evs)
	if !strings.Contains(asked, `"content":"[an image is attached here, not shown to you: cat.png]"`) || strings.Contains(asked, "image_url") {
		t.Errorf("asked %s", asked)
	}
}

func TestWhatNothingCarriesGoesAsWords(t *testing.T) {
	for name, c := range map[string]struct{ part, name, why string }{
		"a video":         {`{"type":"video","source":{"type":"data","value":"AAAA","mimeType":"video/mp4"},"metadata":{"filename":"clip.mp4"}}`, "clip.mp4", "a video"},
		"a sound by link": {`{"type":"audio","source":{"type":"url","value":"https://example.com/a.wav","mimeType":"audio/wav"}}`, "audio/wav", "a sound goes"},
		"an ogg sound":    {`{"type":"audio","source":{"type":"data","value":"AAAA","mimeType":"audio/ogg"},"metadata":{"filename":"v.ogg"}}`, "v.ogg", "a model takes a sound as WAV or MP3"},
		"a PDF by link":   {`{"type":"document","source":{"type":"url","value":"https://example.com/a.pdf","mimeType":"application/pdf"}}`, "application/pdf", "a document goes"},
	} {
		t.Run(name, func(t *testing.T) {
			log := logs(t)
			evs, asked := ask(t, "", c.part)
			wentOn(t, evs)
			want := `"content":"look\n[an attachment is here that could not be sent to you: ` + c.name + `]"`
			if !strings.Contains(asked, want) {
				t.Errorf("asked\n %s\nwant in it\n %s", asked, want)
			}
			for _, w := range []string{"level=WARN", "model=m", "name=" + c.name, c.why} {
				if !strings.Contains(log.String(), w) {
					t.Errorf("log has no %q:\n%s", w, log)
				}
			}
		})
	}
}

func TestAPartNothingCarriesAloneIsAMessage(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"video","source":{"type":"data","value":"AAAA","mimeType":"video/mp4"},"metadata":{"filename":"clip.mp4"}}]}]}`
	if got := string(lastContent(t, body)); got != `"[an attachment is here that could not be sent to you: clip.mp4]"` {
		t.Errorf("content %s", got)
	}
}

func TestASoundAloneIsAMessage(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"audio","source":{"type":"data","value":"AAAA","mimeType":"audio/mpeg"}}]}]}`
	if got := string(lastContent(t, body)); !strings.Contains(got, `"format":"mp3"`) {
		t.Errorf("content %s", got)
	}
}
