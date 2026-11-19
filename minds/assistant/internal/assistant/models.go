package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// The models a person may pick. A model here is what the endpoint answers
// to by name: for LiteLLM a group (one name, one or more deployments behind
// it), which is all a person needs to tell them apart.
//
// Offered is what OpenAI's GET /v1/models lists, which LiteLLM answers with
// the groups the key may use, less those LiteLLM's /v1/model/info says are
// not for chat (a mode other than chat: transcription, speech, realtime,
// embeddings, …). The configured model is always offered, and first: no
// pick means that one. When the endpoint cannot say, only it is offered.
//
// A pick travels in the run as AG-UI's forwardedProps.modelName, where
// assistant-ui's model selector puts it (its ModelContext config goes into
// forwardedProps as it is). A pick not offered ends the run with RUN_ERROR.

// CodeNotOffered is the code of the RUN_ERROR a run picking a model that
// is not offered ends with.
const CodeNotOffered = "model_not_offered"

// offered is the models a person may pick, the configured one first.
func (a *Assistant) offered(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, a.info)
	defer cancel()
	out := []string{a.model}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := a.client.Get(ctx, "models", nil, &list); err != nil {
		slog.Info("which models may be asked is not known", "err", err)
		return out
	}
	notChat := map[string]bool{}
	var info struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				Mode string `json:"mode"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err := a.client.Get(ctx, "model/info", nil, &info); err == nil {
		for _, d := range info.Data {
			if m := d.ModelInfo.Mode; m != "" && m != "chat" {
				notChat[d.ModelName] = true
			}
		}
	}
	for _, m := range list.Data {
		if m.ID != "" && !notChat[m.ID] && !slices.Contains(out, m.ID) {
			out = append(out, m.ID)
		}
	}
	return out
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
	if name == "" || name == a.model {
		return a.model, nil
	}
	if slices.Contains(a.offered(ctx), name) {
		return name, nil
	}
	return "", events.NewRunErrorEvent(fmt.Sprintf("The model %s is not one this assistant offers.", name), events.WithErrorCode(CodeNotOffered), events.WithRunID(runID))
}

// model is one entry of OpenAI's model list.
type model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// Models is GET /v1/models, OpenAI's shape: the models a person may pick,
// the one a run without a pick is answered by first.
func (a *Assistant) Models(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Principal-Id") == "" {
		http.Error(w, "no principal", http.StatusUnauthorized)
		return
	}
	names := a.offered(r.Context())
	data := make([]model, len(names))
	for i, n := range names {
		data[i] = model{ID: n, Object: "model", OwnedBy: "minds/assistant"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
