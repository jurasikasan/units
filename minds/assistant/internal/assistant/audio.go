package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Audio is OpenAI's audio API, served by the mind and handed on to the
// endpoint (LiteLLM): speech to text at POST /v1/audio/transcriptions, text
// to speech at POST /v1/audio/speech. The mind is the door: the client
// never holds the endpoint's key, the model is the mind's to choose, and
// every call is logged. Nothing else is changed on the way: the fields the
// client sent, the sound, the answer and the endpoint's errors pass as they
// are, so any OpenAI client is a client of this.
//
// It is a pass-through, not OpenAI's Go SDK: the SDK would read the request
// into its own types and write it out again, dropping what it does not
// know, and a door should not.
type Audio struct {
	base   string // the endpoint, with /v1
	key    string
	stt    string // the model asked to transcribe
	tts    string // the model asked to speak
	voice  string // the voice when the client names none
	client *http.Client
}

// Largest bodies taken: a sound to transcribe (OpenAI's own limit), and
// words to speak (OpenAI takes 4096 characters; JSON around them).
const (
	MaxSound  = 25 << 20
	MaxSpeech = 1 << 20
)

// NewAudio hands audio calls to an OpenAI-shaped endpoint (base without
// /v1); key is sent as a Bearer token when set. stt and tts are the models
// asked; an empty one leaves that call unserved. voice is used when a
// speech request names none.
func NewAudio(base, key, stt, tts, voice string) *Audio {
	return &Audio{
		base:   strings.TrimRight(base, "/") + "/v1",
		key:    key,
		stt:    stt,
		tts:    tts,
		voice:  voice,
		client: &http.Client{}, // no timeout: a call lasts as long as its client waits
	}
}

// Routes puts the calls this Audio serves on mux.
func (a *Audio) Routes(mux *http.ServeMux) {
	if a.stt != "" {
		mux.HandleFunc("POST /v1/audio/transcriptions", a.transcribe)
	}
	if a.tts != "" {
		mux.HandleFunc("POST /v1/audio/speech", a.speak)
	}
}

// openAIError writes an error in OpenAI's shape, as the endpoint's own are.
func openAIError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": kind, "code": fmt.Sprint(status)}})
}

func tooBig(err error) bool {
	var e *http.MaxBytesError
	return errors.As(err, &e)
}

// transcribe is POST /v1/audio/transcriptions: the client's multipart form
// goes on part by part as it arrives, its model replaced by the mind's.
func (a *Audio) transcribe(w http.ResponseWriter, r *http.Request) {
	principal := r.Header.Get("X-Principal-Id")
	if principal == "" {
		openAIError(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSound)
	in, err := r.MultipartReader()
	if err != nil {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "the body is not a multipart form: "+err.Error())
		return
	}

	// The form is copied into a pipe while the endpoint reads it, so a sound
	// is never held whole; a bad form shows up as the copy's error.
	pr, pw := io.Pipe()
	out := multipart.NewWriter(pw)
	copied := make(chan error, 1)
	var sound int64
	go func() {
		err := func() error {
			if err := out.WriteField("model", a.stt); err != nil {
				return err
			}
			for {
				p, err := in.NextPart()
				if err == io.EOF {
					return out.Close()
				}
				if err != nil {
					return err
				}
				if p.FormName() == "model" {
					continue
				}
				dst, err := out.CreatePart(p.Header)
				if err != nil {
					return err
				}
				n, err := io.Copy(dst, p)
				if p.FileName() != "" {
					sound += n
				}
				if err != nil {
					return err
				}
			}
		}()
		_ = pw.CloseWithError(err)
		copied <- err
	}()

	a.pass(w, r, "transcription", a.stt, out.FormDataContentType(), pr, principal, func() (int64, error) {
		err := <-copied
		return sound, err
	})
}

// speak is POST /v1/audio/speech: the client's JSON with the mind's model,
// and the mind's voice when the client named none.
func (a *Audio) speak(w http.ResponseWriter, r *http.Request) {
	principal := r.Header.Get("X-Principal-Id")
	if principal == "" {
		openAIError(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	var req map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxSpeech)).Decode(&req); err != nil {
		if tooBig(err) {
			openAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", fmt.Sprintf("a speech request is at most %d MB", MaxSpeech>>20))
			return
		}
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "the body is not a JSON object: "+err.Error())
		return
	}
	if req == nil {
		req = map[string]json.RawMessage{}
	}
	req["model"], _ = json.Marshal(a.tts)
	if v, ok := req["voice"]; (!ok || string(v) == `""` || string(v) == "null") && a.voice != "" {
		req["voice"], _ = json.Marshal(a.voice)
	}
	body, _ := json.Marshal(req)
	a.pass(w, r, "speech", a.tts, "application/json", io.NopCloser(bytes.NewReader(body)), principal, func() (int64, error) { return int64(len(body)), nil })
}

// pass sends body to the endpoint's path for kind and streams the answer
// back as it comes: its status, its Content-Type, its bytes. body is closed
// once the endpoint has answered or failed; sent then says how much of it
// was sent and whether reading the client's went wrong.
func (a *Audio) pass(w http.ResponseWriter, r *http.Request, kind, model, contentType string, body io.ReadCloser, principal string, sent func() (int64, error)) {
	start := time.Now()
	path := map[string]string{"transcription": "/audio/transcriptions", "speech": "/audio/speech"}[kind]
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.base+path, body)
	if err != nil {
		openAIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Content-Type", contentType)
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	log := slog.With("call", kind, "principal", principal, "model", model)

	resp, err := a.client.Do(req)
	_ = body.Close() // an endpoint that answered before reading all of it has had its say
	in, readErr := sent()
	if errors.Is(readErr, io.ErrClosedPipe) {
		readErr = nil
	}
	switch {
	case readErr != nil && tooBig(readErr):
		if resp != nil {
			_ = resp.Body.Close()
		}
		log.Info("audio call refused: too large", "bytes_in", in)
		openAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", fmt.Sprintf("a sound is at most %d MB", MaxSound>>20))
		return
	case r.Context().Err() != nil:
		if resp != nil {
			_ = resp.Body.Close()
		}
		log.Info("audio call cancelled: the client went away", "bytes_in", in, "after", time.Since(start))
		return
	case readErr != nil && (err != nil || resp.StatusCode < 400):
		if resp != nil {
			_ = resp.Body.Close()
		}
		log.Info("audio call refused: a bad form", "err", readErr)
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "the form could not be read: "+readErr.Error())
		return
	case err != nil:
		log.Info("audio call failed: the endpoint did not answer", "err", err)
		openAIError(w, http.StatusBadGateway, CodeModelUnreachable, "The model's endpoint did not answer: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	n, copyErr := copyFlushing(w, resp.Body)

	attrs := []any{"status", resp.StatusCode, "answered_by", resp.Header.Get("X-Litellm-Model-Name"), "bytes_in", in, "bytes_out", n, "after", time.Since(start)}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "" {
		attrs = append(attrs, "content_type", mt)
	}
	switch {
	case r.Context().Err() != nil:
		log.Info("audio call cancelled: the client went away", attrs...)
	case copyErr != nil:
		log.Info("audio call broke off", append(attrs, "err", copyErr)...)
	case resp.StatusCode >= 400:
		log.Info("audio call refused by the endpoint", attrs...)
	default:
		log.Info("audio call", attrs...)
	}
}

// copyFlushing copies src to w, handing each piece on as it arrives.
func copyFlushing(w http.ResponseWriter, src io.Reader) (int64, error) {
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var n int64
	for {
		k, err := src.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
			if f != nil {
				f.Flush()
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}
