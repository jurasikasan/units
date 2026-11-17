// Package assistant is the mind: a run in, its events out. A run is a stream
// of events that ends when the answer is whole or when whoever asked goes
// away; the HTTP handler is one way to watch it, not the run itself.
//
// Both edges are standards, through their own libraries: AG-UI toward people
// (the protocol's Go SDK: RunAgentInput, events, SSE), OpenAI's chat
// completions toward models (OpenAI's Go SDK). What lies between is ours.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Assistant answers runs with one model.
type Assistant struct {
	client openai.Client
	model  string
	system string
	sse    *sse.SSEWriter
}

// New is an assistant asking model at an OpenAI-shaped endpoint (base
// without /v1). key is sent as a Bearer token when set; system, when set,
// is first in every prompt. Retries and fallbacks are the endpoint's
// business (LiteLLM's): the client does not retry.
func New(base, model, key, system string) *Assistant {
	opts := []option.RequestOption{
		option.WithBaseURL(strings.TrimRight(base, "/") + "/v1/"),
		option.WithMaxRetries(0),
	}
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	return &Assistant{client: openai.NewClient(opts...), model: model, system: system, sse: sse.NewSSEWriter()}
}

// ErrBadInput is a run this mind cannot answer as asked.
var ErrBadInput = errors.New("the last message must be the user's and say something")

// text is what a message says in words: its content when that is a string,
// or its text parts joined. Other parts (an image, a recording) are passed
// over, not refused.
func text(m types.Message) string {
	if s, ok := m.ContentString(); ok {
		return s
	}
	parts, _ := m.ContentInputContents()
	var words []string
	for _, p := range parts {
		if p.Type == types.InputContentTypeText && p.Text != "" {
			words = append(words, p.Text)
		}
	}
	return strings.Join(words, "\n")
}

// Prompt is what the model is asked: the system prompt, then the
// conversation as the client sent it, text only. The client's history is
// believed, since there is nowhere else to read it from yet.
func (a *Assistant) Prompt(in types.RunAgentInput) ([]openai.ChatCompletionMessageParamUnion, error) {
	n := len(in.Messages)
	if n == 0 || in.Messages[n-1].Role != types.RoleUser || text(in.Messages[n-1]) == "" {
		return nil, ErrBadInput
	}
	var msgs []openai.ChatCompletionMessageParamUnion
	if a.system != "" {
		msgs = append(msgs, openai.SystemMessage(a.system))
	}
	for _, m := range in.Messages {
		t := text(m)
		if t == "" {
			continue
		}
		switch m.Role {
		case types.RoleUser:
			msgs = append(msgs, openai.UserMessage(t))
		case types.RoleAssistant:
			msgs = append(msgs, openai.AssistantMessage(t))
		case types.RoleSystem, types.RoleDeveloper:
			msgs = append(msgs, openai.SystemMessage(t))
		}
		// tool, reasoning, activity: nothing a plain chat reads yet
	}
	return msgs, nil
}

// Run answers one run, sending its events to emit. An error from emit (the
// listener is gone) or a cancelled ctx ends the run and the model call with
// it. A failing model ends the run with RUN_ERROR.
func (a *Assistant) Run(ctx context.Context, in types.RunAgentInput, msgs []openai.ChatCompletionMessageParamUnion, emit func(events.Event) error) error {
	if err := emit(events.NewRunStartedEvent(in.ThreadID, in.RunID)); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream := a.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:    a.model,
		Messages: msgs,
	})
	defer func() { _ = stream.Close() }()

	msgID := events.GenerateMessageID()
	started := false
	for stream.Next() {
		for _, ch := range stream.Current().Choices {
			piece := ch.Delta.Content
			if piece == "" {
				continue
			}
			if !started {
				if err := emit(events.NewTextMessageStartEvent(msgID, events.WithRole("assistant"))); err != nil {
					return err
				}
				started = true
			}
			if err := emit(events.NewTextMessageContentEvent(msgID, piece)); err != nil {
				return err
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := stream.Err(); err != nil {
		return emit(events.NewRunErrorEvent(err.Error(), events.WithRunID(in.RunID)))
	}
	if started {
		if err := emit(events.NewTextMessageEndEvent(msgID)); err != nil {
			return err
		}
	}
	return emit(events.NewRunFinishedEvent(in.ThreadID, in.RunID))
}

// ServeHTTP is POST /run: AG-UI's RunAgentInput in, its events out as SSE.
// Who asks comes in X-Principal-Id, set by the door in front.
func (a *Assistant) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	principal := r.Header.Get("X-Principal-Id")
	if principal == "" {
		http.Error(w, "no principal", http.StatusUnauthorized)
		return
	}
	var in types.RunAgentInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		http.Error(w, "the body is not a RunAgentInput: "+err.Error(), http.StatusBadRequest)
		return
	}
	msgs, err := a.Prompt(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.ThreadID == "" {
		in.ThreadID = events.GenerateThreadID()
	}
	if in.RunID == "" {
		in.RunID = events.GenerateRunID()
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // nginx: pass each event on as it comes
	w.WriteHeader(http.StatusOK)

	emit := func(ev events.Event) error { return a.sse.WriteEvent(r.Context(), w, ev) }
	if err := a.Run(r.Context(), in, msgs, emit); err != nil {
		slog.Info("run ended early", "principal", principal, "thread", in.ThreadID, "run", in.RunID, "err", err)
	}
}
