// Package assistant is the mind: a run in, its events out. A run is a stream
// of events that ends when the answer is whole or when whoever asked goes
// away; the HTTP handler is one way to watch it, not the run itself.
//
// The edges are standards, through their own libraries: AG-UI toward people
// (the protocol's Go SDK: RunAgentInput, events, SSE), OpenAI's chat
// completions toward models (OpenAI's Go SDK), MCP toward tools (the MCP Go
// SDK). What lies between is ours.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// Assistant answers runs with one model.
type Assistant struct {
	client   openai.Client
	model    string
	system   string
	sse      *sse.SSEWriter
	mcp      *mcp.Client
	servers  []Server
	maxSteps int
}

// DefaultMaxSteps is how many times a run asks the model when it keeps
// calling tools, unless UseTools says otherwise.
const DefaultMaxSteps = 8

// UseTools gives the model the tools of these MCP servers. A run asks the
// model at most maxSteps times (DefaultMaxSteps when 0); one that still
// wants tools after that ends with RUN_ERROR.
func (a *Assistant) UseTools(servers []Server, maxSteps int) {
	a.servers = servers
	a.maxSteps = maxSteps
	if a.maxSteps <= 0 {
		a.maxSteps = DefaultMaxSteps
	}
	a.mcp = mcp.NewClient(&mcp.Implementation{Name: "minds/assistant", Version: "dev"}, nil)
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
	return &Assistant{client: openai.NewClient(opts...), model: model, system: system, sse: sse.NewSSEWriter(), maxSteps: DefaultMaxSteps}
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
// conversation as the client sent it: words, the tool calls an answer made
// (their ids as they were) and what the tools said. The client's history is
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
	called := map[string]bool{} // ids of the calls the history has made
	for _, m := range in.Messages {
		t := text(m)
		switch {
		case m.Role == types.RoleAssistant && len(m.ToolCalls) > 0:
			calls := make([]call, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				calls[i] = call{id: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments}
				called[tc.ID] = true
			}
			msgs = append(msgs, answered(t, calls))
		case m.Role == types.RoleTool:
			// A result with no call before it is left out: a model refuses one.
			if called[m.ToolCallID] {
				msgs = append(msgs, openai.ToolMessage(t, m.ToolCallID))
			}
		case t == "":
		case m.Role == types.RoleUser:
			msgs = append(msgs, openai.UserMessage(t))
		case m.Role == types.RoleAssistant:
			msgs = append(msgs, openai.AssistantMessage(t))
		case m.Role == types.RoleSystem, m.Role == types.RoleDeveloper:
			msgs = append(msgs, openai.SystemMessage(t))
		}
		// reasoning, activity: not read yet
	}
	return msgs, nil
}

// call is a tool call as the model made it. The id goes back to the model
// exactly as it came: a provider may keep its own state in it (Gemini puts
// its thought signature there, through LiteLLM).
type call struct {
	id, name, args string
}

// answered is the model's turn as it goes back into the prompt: what it
// said and the calls it made.
func answered(text string, calls []call) openai.ChatCompletionMessageParamUnion {
	m := &openai.ChatCompletionAssistantMessageParam{}
	if text != "" {
		m.Content.OfString = param.NewOpt(text)
	}
	for _, c := range calls {
		m.ToolCalls = append(m.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID:       c.id,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.name, Arguments: c.args},
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: m}
}

// Run answers one run, sending its events to emit. The model is asked; when
// it calls tools, they are called (all of one turn at once), what they say
// goes back to it, and it is asked again, until it answers in words or the
// steps run out. An error from emit (the listener is gone) or a cancelled
// ctx ends the run and whatever call is under way. A failing model ends the
// run with RUN_ERROR.
func (a *Assistant) Run(ctx context.Context, in types.RunAgentInput, msgs []openai.ChatCompletionMessageParamUnion, emit func(events.Event) error) error {
	if err := emit(events.NewRunStartedEvent(in.ThreadID, in.RunID)); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var k *kit
	if len(a.servers) > 0 {
		k = openKit(ctx, a.mcp, a.servers)
		defer k.close()
	}
	for step := 0; step < a.maxSteps; step++ {
		said, calls, err := a.turn(ctx, in, msgs, k, emit)
		if err != nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if said == nil {
			return nil // the turn failed and said so
		}
		if len(calls) == 0 {
			return emit(events.NewRunFinishedEvent(in.ThreadID, in.RunID))
		}
		msgs = append(msgs, answered(*said, calls))
		results := k.callAll(ctx, calls)
		for i, c := range calls {
			if err := emit(events.NewToolCallResultEvent(events.GenerateMessageID(), c.id, results[i])); err != nil {
				return err
			}
			msgs = append(msgs, openai.ToolMessage(results[i], c.id))
		}
	}
	return emit(events.NewRunErrorEvent(fmt.Sprintf("the model was still calling tools after %d steps", a.maxSteps), events.WithRunID(in.RunID)))
}

// turn asks the model once and streams what it says and the tools it calls.
// It returns the words and the calls; said is nil when the model failed and
// RUN_ERROR has been sent.
func (a *Assistant) turn(ctx context.Context, in types.RunAgentInput, msgs []openai.ChatCompletionMessageParamUnion, k *kit, emit func(events.Event) error) (said *string, calls []call, err error) {
	params := openai.ChatCompletionNewParams{Model: a.model, Messages: msgs}
	if k != nil && len(k.tools) > 0 {
		params.Tools = k.tools
	}
	stream := a.client.Chat.Completions.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	msgID := events.GenerateMessageID()
	var words strings.Builder
	started := false
	byIndex := map[int64]int{} // a call's index in the stream → its place in calls
	for stream.Next() {
		for _, ch := range stream.Current().Choices {
			if piece := ch.Delta.Content; piece != "" {
				if !started {
					if err := emit(events.NewTextMessageStartEvent(msgID, events.WithRole("assistant"))); err != nil {
						return nil, nil, err
					}
					started = true
				}
				words.WriteString(piece)
				if err := emit(events.NewTextMessageContentEvent(msgID, piece)); err != nil {
					return nil, nil, err
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				n, seen := byIndex[tc.Index]
				if !seen {
					n = len(calls)
					byIndex[tc.Index] = n
					calls = append(calls, call{id: tc.ID, name: tc.Function.Name})
					if err := emit(events.NewToolCallStartEvent(tc.ID, tc.Function.Name, events.WithParentMessageID(msgID))); err != nil {
						return nil, nil, err
					}
				}
				if d := tc.Function.Arguments; d != "" {
					calls[n].args += d
					if err := emit(events.NewToolCallArgsEvent(calls[n].id, d)); err != nil {
						return nil, nil, err
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err := stream.Err(); err != nil {
		return nil, nil, emit(events.NewRunErrorEvent(err.Error(), events.WithRunID(in.RunID)))
	}
	if started {
		if err := emit(events.NewTextMessageEndEvent(msgID)); err != nil {
			return nil, nil, err
		}
	}
	for _, c := range calls {
		if err := emit(events.NewToolCallEndEvent(c.id)); err != nil {
			return nil, nil, err
		}
	}
	s := words.String()
	return &s, calls, nil
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
