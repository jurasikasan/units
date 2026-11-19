package assistant

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAudio is an OpenAI audio endpoint that keeps what it was sent and
// answers as told.
type fakeAudio struct {
	mu     sync.Mutex
	path   string
	auth   string
	fields map[string]string // a form's fields, or a JSON body's
	file   []byte
	name   string
	status int
	ctype  string
	answer string
}

// The fake endpoint's catalog: LiteLLM's lists, and an engine's voices
// through its pass-through.
var audioCatalog = map[string]string{
	"/v1/models":              `{"data":[{"id":"chat-model"},{"id":"stt-model"},{"id":"other-stt"},{"id":"tts-model"},{"id":"other-tts"}]}`,
	"/v1/model/info":          `{"data":[{"model_name":"stt-model","model_info":{"mode":"audio_transcription"}},{"model_name":"other-stt","model_info":{"mode":"audio_transcription"}},{"model_name":"tts-model","model_info":{"mode":"audio_speech"}},{"model_name":"other-tts","model_info":{"mode":"audio_speech"}}]}`,
	"/audio/voices/other-tts": `{"voices":[{"id":"denis","gender":"male"},{"id":"irina"}]}`,
}

func (f *fakeAudio) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if b, ok := audioCatalog[r.URL.Path]; ok {
			_, _ = io.WriteString(w, b)
			return
		}
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.path, f.auth, f.fields = r.URL.Path, r.Header.Get("Authorization"), map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FileName() != "" {
				f.file, f.name = b, p.FileName()
			} else {
				f.fields[p.FormName()] = string(b)
			}
		}
	} else {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		for k, v := range m {
			f.fields[k] = toString(v)
		}
	}
	if f.ctype != "" {
		w.Header().Set("Content-Type", f.ctype)
	}
	w.Header().Set("X-Litellm-Model-Name", "provider/model")
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	_, _ = io.WriteString(w, f.answer)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func audioMind(t *testing.T, f http.Handler, voice string) string {
	t.Helper()
	end := httptest.NewServer(f)
	t.Cleanup(end.Close)
	mux := http.NewServeMux()
	c := NewCatalog(end.URL, "k", "chat-model", "", "", "")
	c.Sound("stt-model", "tts-model", voice)
	NewAudio(end.URL, "k", c).Routes(mux)
	mind := httptest.NewServer(mux)
	t.Cleanup(mind.Close)
	return mind.URL
}

func form(t *testing.T, fields map[string]string, sound []byte) (string, *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if sound != nil {
		p, _ := w.CreateFormFile("file", "hello.webm")
		_, _ = p.Write(sound)
	}
	_ = w.Close()
	return w.FormDataContentType(), &b
}

func postAudio(t *testing.T, url, principal, ctype string, body io.Reader) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", ctype)
	if principal != "" {
		req.Header.Set("X-Principal-Id", principal)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestTranscriptionGoesOnWithTheMindsModel(t *testing.T) {
	f := &fakeAudio{ctype: "application/json", answer: `{"text":"hello"}`}
	url := audioMind(t, f, "")
	sound := bytes.Repeat([]byte{1, 2, 3, 0}, 50000)
	ct, body := form(t, map[string]string{"language": "ru", "response_format": "verbose_json"}, sound)

	resp, got := postAudio(t, url+"/v1/audio/transcriptions", "alice", ct, body)
	if resp.StatusCode != 200 || got != `{"text":"hello"}` || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("answer: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}
	if f.path != "/v1/audio/transcriptions" || f.auth != "Bearer k" {
		t.Fatalf("sent to %q with %q", f.path, f.auth)
	}
	if f.fields["model"] != "stt-model" || f.fields["language"] != "ru" || f.fields["response_format"] != "verbose_json" {
		t.Fatalf("fields: %v", f.fields)
	}
	if !bytes.Equal(f.file, sound) || f.name != "hello.webm" {
		t.Fatalf("the sound changed on the way: %d bytes, %q", len(f.file), f.name)
	}
}

func TestSpeechGoesOnWithTheMindsModelAndVoice(t *testing.T) {
	f := &fakeAudio{ctype: "audio/wav", answer: "RIFF....WAVE"}
	url := audioMind(t, f, "Kore")

	resp, got := postAudio(t, url+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`{"input":"Hi.","speed":1.5}`))
	if resp.StatusCode != 200 || got != "RIFF....WAVE" || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("answer: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}
	if f.path != "/v1/audio/speech" || f.fields["model"] != "tts-model" || f.fields["voice"] != "Kore" || f.fields["input"] != "Hi." || f.fields["speed"] != "1.5" {
		t.Fatalf("sent %q %v", f.path, f.fields)
	}

	// A voice the client names is its own.
	postAudio(t, url+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`{"input":"Hi.","voice":"Puck"}`))
	if f.fields["voice"] != "Puck" {
		t.Fatalf("voice: %v", f.fields)
	}
}

func TestAudioEndpointErrorsPassAsTheyAre(t *testing.T) {
	refusal := `{"error":{"message":"Gemini TTS only produces raw PCM16 audio","type":null,"code":"400"}}`
	f := &fakeAudio{status: 400, ctype: "application/json", answer: refusal}
	url := audioMind(t, f, "")
	resp, got := postAudio(t, url+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`{"input":"Hi.","response_format":"mp3"}`))
	if resp.StatusCode != 400 || got != refusal {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
}

func TestAudioWhoMayAskAndHowMuch(t *testing.T) {
	url := audioMind(t, &fakeAudio{answer: "{}"}, "")

	resp, _ := postAudio(t, url+"/v1/audio/speech", "", "application/json", strings.NewReader(`{"input":"Hi."}`))
	if resp.StatusCode != 401 {
		t.Fatalf("nobody named: %d", resp.StatusCode)
	}
	ct, body := form(t, nil, []byte("x"))
	resp, _ = postAudio(t, url+"/v1/audio/transcriptions", "", ct, body)
	if resp.StatusCode != 401 {
		t.Fatalf("nobody named: %d", resp.StatusCode)
	}

	ct, body = form(t, nil, make([]byte, MaxSound+1))
	resp, got := postAudio(t, url+"/v1/audio/transcriptions", "alice", ct, body)
	if resp.StatusCode != 413 || !strings.Contains(got, "at most 25 MB") {
		t.Fatalf("too large: %d %s", resp.StatusCode, got)
	}
	resp, _ = postAudio(t, url+"/v1/audio/transcriptions", "alice", "application/json", strings.NewReader(`{}`))
	if resp.StatusCode != 400 {
		t.Fatalf("not a form: %d", resp.StatusCode)
	}
	resp, _ = postAudio(t, url+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`not json`))
	if resp.StatusCode != 400 {
		t.Fatalf("not JSON: %d", resp.StatusCode)
	}
}

func TestAudioEndpointUnreachable(t *testing.T) {
	end := httptest.NewServer(http.NotFoundHandler())
	end.Close()
	mux := http.NewServeMux()
	c := NewCatalog(end.URL, "", "m", "", "", "")
	c.Sound("s", "t", "")
	NewAudio(end.URL, "", c).Routes(mux)
	mind := httptest.NewServer(mux)
	defer mind.Close()

	ct, body := form(t, nil, bytes.Repeat([]byte("x"), 1<<20))
	resp, got := postAudio(t, mind.URL+"/v1/audio/transcriptions", "alice", ct, body)
	if resp.StatusCode != 502 || !strings.Contains(got, CodeModelUnreachable) {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
}

func TestAudioUnconfiguredIsNotServed(t *testing.T) {
	mux := http.NewServeMux()
	NewAudio("http://nowhere", "", NewCatalog("http://nowhere", "", "m", "", "", "")).Routes(mux)
	mind := httptest.NewServer(mux)
	defer mind.Close()
	resp, _ := postAudio(t, mind.URL+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`{"input":"Hi."}`))
	if resp.StatusCode != 404 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestAPickedSoundModelIsTheOneAsked(t *testing.T) {
	f := &fakeAudio{ctype: "application/json", answer: `{"text":"hello"}`}
	url := audioMind(t, f, "Kore")

	// The model before the sound, as OpenAI's clients send it.
	ct, body := form(t, map[string]string{"model": "other-stt", "language": "ru"}, []byte("sound"))
	if resp, got := postAudio(t, url+"/v1/audio/transcriptions", "alice", ct, body); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
	if f.fields["model"] != "other-stt" || f.fields["language"] != "ru" || string(f.file) != "sound" {
		t.Fatalf("sent %v %q", f.fields, f.file)
	}

	// Another speech model: its own voice, never the mind's default one's.
	postAudio(t, url+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`{"model":"other-tts","input":"Hi."}`))
	if f.fields["model"] != "other-tts" || f.fields["voice"] != "" {
		t.Fatalf("sent %v", f.fields)
	}
	postAudio(t, url+"/v1/audio/speech", "alice", "application/json", strings.NewReader(`{"model":"other-tts","input":"Hi.","voice":"irina"}`))
	if f.fields["model"] != "other-tts" || f.fields["voice"] != "irina" {
		t.Fatalf("sent %v", f.fields)
	}
}

func TestASoundModelNotOfferedIsRefused(t *testing.T) {
	f := &fakeAudio{answer: "{}"}
	url := audioMind(t, f, "")
	for _, c := range []struct{ path, model string }{
		{"/v1/audio/transcriptions", "whisper-1"},
		{"/v1/audio/transcriptions", "tts-model"}, // offered, but to speak
		{"/v1/audio/speech", "tts-1"},
		{"/v1/audio/speech", "chat-model"},
	} {
		f.path = ""
		var resp *http.Response
		var got string
		if strings.HasSuffix(c.path, "transcriptions") {
			ct, body := form(t, map[string]string{"model": c.model}, []byte("x"))
			resp, got = postAudio(t, url+c.path, "alice", ct, body)
		} else {
			resp, got = postAudio(t, url+c.path, "alice", "application/json", strings.NewReader(`{"input":"Hi.","model":"`+c.model+`"}`))
		}
		if resp.StatusCode != 400 || !strings.Contains(got, CodeNotOffered) || !strings.Contains(got, c.model) || f.path != "" {
			t.Errorf("%s %s: %d %s, sent to %q", c.path, c.model, resp.StatusCode, got, f.path)
		}
	}
}
