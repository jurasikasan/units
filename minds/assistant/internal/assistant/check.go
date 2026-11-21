package assistant

import (
	"context"
	"log/slog"
)

// Before a model is asked, the run looks at what the conversation carries
// beyond words: the whole conversation goes with every run, so a picture said
// three turns ago is sent again now. A part nothing carries, or one the model
// cannot take, goes as a placeholder in words where it stood (parts.go), and
// the run goes on: a mismatch between the conversation and the model is a
// warning in the log, not a failed run, nor a part left out unsaid or
// refused by a provider with a 400 in its own words. AG-UI has no event for
// a warning that is not the end of a run, so the page is not told.
//
// What a model takes is LiteLLM's to know: GET /v1/model/info (an extension
// of LiteLLM's, not OpenAI's) gives each deployment's supports_vision,
// supports_pdf_input and supports_audio_input, from LiteLLM's map of models
// or from model_info in its config. A model name LiteLLM routes to several
// deployments takes only what all of them do, since any may answer. When
// nothing says (no such endpoint, a model LiteLLM does not know, a flag not
// set), the part is sent as before and the model has the last word.

// field is LiteLLM's model_info flag that says a model takes a kind.
var field = map[kind]string{
	kindImage: "supports_vision",
	kindPDF:   "supports_pdf_input",
	kindAudio: "supports_audio_input",
}

// fit is the conversation as group is asked it: each part group does not
// take goes as its placeholder, and each such part, or one nothing carries,
// is warned of.
func (a *Assistant) fit(ctx context.Context, group string, p Conversation) Conversation {
	var kinds []kind
	for _, e := range p {
		for _, pc := range e.pieces {
			if m := pc.media; m != nil && m.kind != "" {
				kinds = append(kinds, m.kind)
			}
		}
	}
	refused := map[kind]bool{}
	if len(kinds) > 0 {
		takes := a.takes(ctx, group)
		for _, k := range kinds {
			refused[k] = takes[k] != nil && !*takes[k]
		}
	}
	out := make(Conversation, len(p))
	for i, e := range p {
		out[i] = e
		changed := false
		for _, pc := range e.pieces {
			switch m := pc.media; {
			case m == nil:
			case m.kind == "":
				slog.Warn("a part nothing carries goes as words", "model", group, "kind", "not carried", "name", m.name, "why", m.why)
			case refused[m.kind]:
				slog.Warn("a part the model does not take goes as words", "model", group, "kind", string(m.kind), "name", m.name)
				changed = true
			}
		}
		if changed {
			out[i].msg = userContent(e.pieces, refused)
		}
	}
	return out
}

// takes is what the model takes, kind by kind, as LiteLLM tells: true,
// false, or nil for not known.
func (a *Assistant) takes(ctx context.Context, group string) map[kind]*bool {
	ctx, cancel := context.WithTimeout(ctx, a.info)
	defer cancel()
	var info struct {
		Data []struct {
			ModelName string         `json:"model_name"`
			ModelInfo map[string]any `json:"model_info"`
		} `json:"data"`
	}
	if err := a.client.Get(ctx, "model/info", nil, &info); err != nil {
		slog.Info("what the model takes is not known", "model", group, "err", err)
		return nil
	}
	out := map[kind]*bool{}
	for k, f := range field {
		seen, all := 0, true
		var no bool
		for _, d := range info.Data {
			if d.ModelName != group {
				continue
			}
			seen++
			switch v, ok := d.ModelInfo[f].(bool); {
			case !ok:
				all = false
			case !v:
				no = true
			}
		}
		switch {
		case no:
			out[k] = new(false)
		case seen > 0 && all:
			out[k] = new(true)
		}
	}
	return out
}
