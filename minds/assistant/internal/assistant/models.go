package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// The models a person may pick. A model here is what the endpoint answers
// to by name: for LiteLLM a group (one name, one or more deployments behind
// it), which is all a person needs to tell them apart.
//
// Offered is what OpenAI's GET /v1/models lists, which LiteLLM answers with
// the groups the key may use, each sorted by its mode in LiteLLM's
// /v1/model/info: chat (or no mode said), audio_transcription,
// audio_speech; any other (realtime, embeddings, …) is not offered. The
// configured model of a mode is always offered, and first of it: no pick
// means that one. A mode with no configured model is not served, and none
// of it is offered. When the endpoint cannot say, only the configured ones
// are offered.
//
// A speech model's voices are asked at the endpoint's /audio/voices/<model>
// (LiteLLM: a pass-through to the engine's own GET /v1/audio/voices, which
// OpenAI does not have): {"voices":[{"id"}]}. None there: no list, and the
// voice is the client's word.

// Modes of a model, LiteLLM's names.
const (
	ModeChat       = "chat"
	ModeTranscribe = "audio_transcription"
	ModeSpeak      = "audio_speech"
)

// CodeNotOffered is the code of the error a pick that is not offered ends
// with: a run's RUN_ERROR, an audio call's 400.
const CodeNotOffered = "model_not_offered"

// Offer is one model a person may pick.
type Offer struct {
	ID     string
	Mode   string
	Voices []string // a speech model's, its default first; nil when not known
}

// Catalog knows what is offered, asking the endpoint each time.
type Catalog struct {
	base   string // the endpoint, without /v1
	key    string
	client *http.Client
	wait   time.Duration
	byMode map[string]string // the configured model of each mode
	voice  string            // the configured voice of the speech model
}

// NewCatalog asks base (without /v1) with key. chat, stt and tts are the
// models a run, a transcription and a speech go to without a pick (empty:
// that mode is not served); voice is tts's voice when none is named.
func NewCatalog(base, key, chat, stt, tts, voice string) *Catalog {
	byMode := map[string]string{}
	for m, id := range map[string]string{ModeChat: chat, ModeTranscribe: stt, ModeSpeak: tts} {
		if id != "" {
			byMode[m] = id
		}
	}
	return &Catalog{base: strings.TrimRight(base, "/"), key: key, client: &http.Client{}, wait: 5 * time.Second, byMode: byMode, voice: voice}
}

// Sound serves transcription and speech too: stt and tts are the models
// asked without a pick (empty: that call not served), voice tts's voice.
func (c *Catalog) Sound(stt, tts, voice string) {
	for m, id := range map[string]string{ModeTranscribe: stt, ModeSpeak: tts} {
		if id != "" {
			c.byMode[m] = id
		}
	}
	c.voice = voice
}

// Default is the configured model of mode; "" when that mode is not served.
func (c *Catalog) Default(mode string) string { return c.byMode[mode] }

// Voice is the configured voice of the configured speech model.
func (c *Catalog) Voice() string { return c.voice }

func (c *Catalog) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// List is what is offered: chat, then transcription, then speech, each
// mode's configured model first of it.
func (c *Catalog) List(ctx context.Context) []Offer {
	ctx, cancel := context.WithTimeout(ctx, c.wait)
	defer cancel()
	modeOf := map[string]string{}
	var names []string
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/v1/models", &list); err != nil {
		slog.Info("which models may be asked is not known", "err", err)
	} else {
		var info struct {
			Data []struct {
				ModelName string `json:"model_name"`
				ModelInfo struct {
					Mode string `json:"mode"`
				} `json:"model_info"`
			} `json:"data"`
		}
		if err := c.get(ctx, "/v1/model/info", &info); err == nil {
			for _, d := range info.Data {
				if m := d.ModelInfo.Mode; m != "" {
					modeOf[d.ModelName] = m
				}
			}
		}
		for _, m := range list.Data {
			if m.ID != "" && !slices.Contains(names, m.ID) {
				names = append(names, m.ID)
			}
		}
	}
	var out []Offer
	for _, mode := range []string{ModeChat, ModeTranscribe, ModeSpeak} {
		def := c.byMode[mode]
		if def == "" {
			continue
		}
		out = append(out, Offer{ID: def, Mode: mode})
		for _, n := range names {
			m := modeOf[n]
			if m == "" {
				m = ModeChat
			}
			if m == mode && n != def {
				out = append(out, Offer{ID: n, Mode: mode})
			}
		}
	}
	c.voices(ctx, out)
	return out
}

// voices fills in each speech model's voices, asked all at once.
func (c *Catalog) voices(ctx context.Context, offers []Offer) {
	var wg sync.WaitGroup
	for i := range offers {
		o := &offers[i]
		if o.Mode != ModeSpeak {
			continue
		}
		wg.Go(func() {
			var v struct {
				Voices []struct {
					ID string `json:"id"`
				} `json:"voices"`
			}
			if err := c.get(ctx, "/audio/voices/"+o.ID, &v); err == nil {
				for _, x := range v.Voices {
					if x.ID != "" {
						o.Voices = append(o.Voices, x.ID)
					}
				}
			}
			if o.ID != c.byMode[ModeSpeak] || c.voice == "" {
				return
			}
			// The configured voice first, where the list has it or where
			// there is no list.
			if i := slices.Index(o.Voices, c.voice); i > 0 {
				o.Voices = append([]string{c.voice}, slices.Delete(o.Voices, i, i+1)...)
			} else if len(o.Voices) == 0 {
				o.Voices = []string{c.voice}
			}
		})
	}
	wg.Wait()
}

// Pick is the model of mode a request is answered by: picked when it is
// offered, the configured one when nothing is picked. ok is false for a
// pick that is not offered.
func (c *Catalog) Pick(ctx context.Context, mode, picked string) (model string, ok bool) {
	def := c.byMode[mode]
	if picked == "" || picked == def {
		return def, true
	}
	for _, o := range c.List(ctx) {
		if o.Mode == mode && o.ID == picked {
			return picked, true
		}
	}
	return "", false
}

func notOffered(name string) string {
	return fmt.Sprintf("The model %s is not one this assistant offers.", name)
}

// picked is the model a run asks for in forwardedProps.modelName; "" when
// it names none.
func picked(in any) string {
	props, ok := in.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := props["modelName"].(string)
	return name
}

// modelFor is the model a run is answered by: its pick, or the configured
// one. A pick not offered is the RUN_ERROR the run ends with.
func (a *Assistant) modelFor(ctx context.Context, runID string, forwarded any) (string, events.Event) {
	name := picked(forwarded)
	if m, ok := a.catalog.Pick(ctx, ModeChat, name); ok {
		return m, nil
	}
	return "", events.NewRunErrorEvent(notOffered(name), events.WithErrorCode(CodeNotOffered), events.WithRunID(runID))
}

// model is one entry of OpenAI's model list, with what it is for (mode,
// LiteLLM's word) and, for a speech model, its voices: neither is OpenAI's.
type model struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	OwnedBy string   `json:"owned_by"`
	Mode    string   `json:"mode"`
	Voices  []string `json:"voices,omitempty"`
}

// Models is GET /v1/models, OpenAI's shape: the models a person may pick,
// each mode's configured one first of it.
func (c *Catalog) Models(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Principal-Id") == "" {
		http.Error(w, "no principal", http.StatusUnauthorized)
		return
	}
	offers := c.List(r.Context())
	data := make([]model, len(offers))
	for i, o := range offers {
		data[i] = model{ID: o.ID, Object: "model", OwnedBy: "minds/assistant", Mode: o.Mode, Voices: o.Voices}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
