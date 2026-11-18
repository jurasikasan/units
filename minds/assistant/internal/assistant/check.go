package assistant

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// Before a model is asked, the run looks at what the conversation carries
// beyond words: the whole conversation goes with every run, so a picture said
// three turns ago is sent again now. A part nothing carries, or one the model
// cannot take, ends the run with RUN_ERROR the person reads, instead of being
// left out unsaid or refused by a provider with a 400 in its own words.
//
// What a model takes is LiteLLM's to know: GET /v1/model/info (an extension
// of LiteLLM's, not OpenAI's) gives each deployment's supports_vision,
// supports_pdf_input and supports_audio_input, from LiteLLM's map of models
// or from model_info in its config. A model name LiteLLM routes to several
// deployments takes only what all of them do, since any may answer. When
// nothing says (no such endpoint, a model LiteLLM does not know, a flag not
// set), the part is sent as before and the model has the last word.

// Codes of the RUN_ERROR a refused run ends with.
const (
	CodeNotAccepted = "content_not_accepted" // the model cannot take a part
	CodeNotCarried  = "content_not_carried"  // nothing carries a part to a model
)

// field is LiteLLM's model_info flag that says a model takes a kind.
var field = map[kind]string{
	kindImage: "supports_vision",
	kindPDF:   "supports_pdf_input",
	kindAudio: "supports_audio_input",
}

var plural = map[kind]string{kindImage: "pictures", kindPDF: "PDFs", kindAudio: "sounds"}

// check is the RUN_ERROR a run ends with before the model is asked, nil when
// everything in the conversation may go.
func (a *Assistant) check(ctx context.Context, runID string, p Conversation) events.Event {
	var notCarried []string
	byKind := map[kind][]string{}
	for _, e := range p {
		for _, m := range e.media {
			if m.kind == "" {
				notCarried = append(notCarried, m.name+": "+m.why)
			} else if !slices.Contains(byKind[m.kind], m.name) {
				byKind[m.kind] = append(byKind[m.kind], m.name)
			}
		}
	}
	if len(notCarried) > 0 {
		return events.NewRunErrorEvent("Not sent to the model: "+strings.Join(notCarried, "; ")+".", events.WithErrorCode(CodeNotCarried), events.WithRunID(runID))
	}
	if len(byKind) == 0 {
		return nil
	}
	takes := a.takes(ctx)
	var refused []string
	for _, k := range []kind{kindImage, kindPDF, kindAudio} {
		if names := byKind[k]; len(names) > 0 && takes[k] != nil && !*takes[k] {
			refused = append(refused, fmt.Sprintf("%s (%s)", plural[k], strings.Join(names, ", ")))
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return events.NewRunErrorEvent(fmt.Sprintf("The model %s does not take %s.", a.model, strings.Join(refused, " or ")), events.WithErrorCode(CodeNotAccepted), events.WithRunID(runID))
}

// takes is what the model takes, kind by kind, as LiteLLM tells: true,
// false, or nil for not known.
func (a *Assistant) takes(ctx context.Context) map[kind]*bool {
	ctx, cancel := context.WithTimeout(ctx, a.info)
	defer cancel()
	var info struct {
		Data []struct {
			ModelName string         `json:"model_name"`
			ModelInfo map[string]any `json:"model_info"`
		} `json:"data"`
	}
	if err := a.client.Get(ctx, "model/info", nil, &info); err != nil {
		slog.Info("what the model takes is not known", "model", a.model, "err", err)
		return nil
	}
	out := map[kind]*bool{}
	for k, f := range field {
		seen, all := 0, true
		var no bool
		for _, d := range info.Data {
			if d.ModelName != a.model {
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
