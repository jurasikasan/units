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
// (their ids as they were), what the tools said, and the opaque part of a
// turn's reasoning (a reasoning message's encryptedValue, or the answer's
// own, naming the answer it belongs to). The client's history is believed, since there is nowhere
// else to read it from yet.
func (a *Assistant) Prompt(in types.RunAgentInput) (Conversation, error) {
	n := len(in.Messages)
	if n == 0 || in.Messages[n-1].Role != types.RoleUser || text(in.Messages[n-1]) == "" {
		return nil, ErrBadInput
	}
	var p Conversation
	if a.system != "" {
		p = append(p, entry{msg: openai.SystemMessage(a.system)})
	}
	// Opaque parts are found by the answer they name, wherever the client
	// put them: a turn's opaque part is whole only when the model has
	// finished, so it reaches the client after the turn's words, and a
	// client gives it back where it came. By the answer's id, else by its
	// calls.
	byID := map[string]*opaque{}
	for _, m := range in.Messages {
		if o := unseal(m.EncryptedValue); o != nil && (m.Role == types.RoleReasoning || m.Role == types.RoleAssistant) {
			if o.Message != "" {
				byID[o.Message] = o
			}
			for id := range o.Calls {
				byID[id] = o
			}
		}
	}
	thoughtOf := func(m types.Message) *opaque {
		if o := byID[m.ID]; o != nil && m.ID != "" {
			return o
		}
		for _, tc := range m.ToolCalls {
			if o := byID[tc.ID]; o != nil {
				return o
			}
		}
		return nil
	}
	called := map[string]bool{} // ids of the calls the history has made
	for _, m := range in.Messages {
		t := text(m)
		switch {
		case m.Role == types.RoleReasoning:
			// The words were the person's to read; the opaque part was
			// found above.
		case m.Role == types.RoleAssistant && (t != "" || len(m.ToolCalls) > 0):
			s := &said{text: t, thought: thoughtOf(m)}
			for _, tc := range m.ToolCalls {
				s.calls = append(s.calls, call{id: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments})
				called[tc.ID] = true
			}
			p = append(p, entry{said: s})
		case m.Role == types.RoleTool:
			// A result with no call before it is left out: a model refuses one.
			if called[m.ToolCallID] {
				p = append(p, entry{msg: openai.ToolMessage(t, m.ToolCallID)})
			}
		case t == "":
		case m.Role == types.RoleUser:
			p = append(p, entry{msg: openai.UserMessage(t)})
		case m.Role == types.RoleSystem, m.Role == types.RoleDeveloper:
			p = append(p, entry{msg: openai.SystemMessage(t)})
		}
		// activity: not read yet
	}
	return p, nil
}

// Conversation is the prompt kept apart enough to be told again to
// whichever model answers next: the model's own turns stay turns, so the
// opaque part of each goes only to the model that made it.
type Conversation []entry

type entry struct {
	msg  openai.ChatCompletionMessageParamUnion // a message of anyone but the model
	said *said                                  // or a turn of the model
}

// said is a turn of the model: its words, its calls, and the opaque part of
// its reasoning.
type said struct {
	text    string
	calls   []call
	thought *opaque
}

// For is the conversation as model is asked it: every turn's opaque part
// that model made goes with it, any other model's is left out.
func (p Conversation) For(model string) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, len(p))
	for i, e := range p {
		if e.said == nil {
			out[i] = e.msg
			continue
		}
		var thought *opaque
		if !e.said.thought.empty() && e.said.thought.Model == model {
			thought = e.said.thought
		}
		out[i] = answered(e.said.text, e.said.calls, thought)
	}
	return out
}

// lastModel is who made the latest opaque part in the conversation: the
// model expected to answer next, as far as the history knows.
func (p Conversation) lastModel() string {
	for i := len(p) - 1; i >= 0; i-- {
		if s := p[i].said; s != nil && !s.thought.empty() {
			return s.thought.Model
		}
	}
	return ""
}

// call is a tool call as the model made it. The id goes back to the model
// exactly as it came: a provider may keep its own state in it (Gemini puts
// its thought signature there, through LiteLLM).
type call struct {
	id, name, args string
}

// answered is the model's turn as it goes back into the prompt: what it
// said, the calls it made, and, for the model that made it, the opaque part
// of its reasoning, in the fields LiteLLM reads back: thinking_blocks and
// provider_specific_fields on the message and on each call.
func answered(text string, calls []call, thought *opaque) openai.ChatCompletionMessageParamUnion {
	m := &openai.ChatCompletionAssistantMessageParam{}
	if text != "" {
		m.Content.OfString = param.NewOpt(text)
	}
	if thought != nil {
		extras := map[string]any{}
		if len(thought.Blocks) > 0 {
			extras["thinking_blocks"] = thought.Blocks
		}
		if len(thought.Fields) > 0 {
			extras["provider_specific_fields"] = thought.Fields
		}
		if len(extras) > 0 {
			m.SetExtraFields(extras)
		}
	}
	for _, c := range calls {
		fc := &openai.ChatCompletionMessageFunctionToolCallParam{
			ID:       c.id,
			Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.name, Arguments: c.args},
		}
		if psf := thought.callFields(c.id); psf != nil {
			fc.SetExtraFields(map[string]any{"provider_specific_fields": psf})
		}
		m.ToolCalls = append(m.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: fc})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: m}
}

// Run answers one run, sending its events to emit. The model is asked; when
// it calls tools, they are called (all of one turn at once), what they say
// goes back to it, and it is asked again, until it answers in words or the
// steps run out. Each time it is asked as the model that answered last: the
// opaque part of reasoning another model made is left out, so a fallback to
// another model carries on without it. An error from emit (the listener is
// gone) or a cancelled ctx ends the run and whatever call is under way. A
// failing model ends the run with RUN_ERROR.
func (a *Assistant) Run(ctx context.Context, in types.RunAgentInput, p Conversation, emit func(events.Event) error) error {
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
	model := p.lastModel()
	for step := 0; step < a.maxSteps; step++ {
		s, err := a.turn(ctx, in, p.For(model), k, emit)
		if err != nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if s == nil {
			return nil // the turn failed and said so
		}
		if len(s.calls) == 0 {
			return emit(events.NewRunFinishedEvent(in.ThreadID, in.RunID))
		}
		model = s.thought.Model
		p = append(p, entry{said: s})
		results := k.callAll(ctx, s.calls)
		for i, c := range s.calls {
			if err := emit(events.NewToolCallResultEvent(events.GenerateMessageID(), c.id, results[i])); err != nil {
				return err
			}
			p = append(p, entry{msg: openai.ToolMessage(results[i], c.id)})
		}
	}
	return emit(events.NewRunErrorEvent(fmt.Sprintf("the model was still calling tools after %d steps", a.maxSteps), events.WithRunID(in.RunID)))
}

// turn asks the model once and streams what it thinks, says and the tools
// it calls: reasoning first as AG-UI reasoning, then the words, then the
// calls; the opaque part of the reasoning, if any, at the end of the turn as
// REASONING_ENCRYPTED_VALUE on the turn's reasoning message (an empty one
// when the model showed none), before any tool has answered, so the client
// keeps it with this turn. It returns what the model said, its thought
// always naming who answered; nil when the model failed and RUN_ERROR has
// been sent.
func (a *Assistant) turn(ctx context.Context, in types.RunAgentInput, msgs []openai.ChatCompletionMessageParamUnion, k *kit, emit func(events.Event) error) (*said, error) {
	params := openai.ChatCompletionNewParams{Model: a.model, Messages: msgs}
	if k != nil && len(k.tools) > 0 {
		params.Tools = k.tools
	}
	var resp *http.Response
	stream := a.client.Chat.Completions.NewStreaming(ctx, params, option.WithResponseInto(&resp))
	defer func() { _ = stream.Close() }()

	msgID := events.GenerateMessageID()
	s := &said{thought: &opaque{Message: msgID}}
	var words strings.Builder
	started := false
	var thinking, lastThought string // the reasoning message open now, and the turn's latest
	stopThinking := func() error {
		if thinking == "" {
			return nil
		}
		id := thinking
		thinking = ""
		if err := emit(events.NewReasoningMessageEndEvent(id)); err != nil {
			return err
		}
		return emit(events.NewReasoningEndEvent(id))
	}
	chunkModel := ""
	byIndex := map[int64]int{} // a call's index in the stream → its place in calls
	for stream.Next() {
		chunk := stream.Current()
		if chunkModel == "" {
			chunkModel = chunk.Model
		}
		for _, ch := range chunk.Choices {
			if piece := reasoningText(ch.Delta.JSON.ExtraFields); piece != "" {
				if thinking == "" {
					thinking = events.GenerateMessageID()
					lastThought = thinking
					if err := emit(events.NewReasoningStartEvent(thinking)); err != nil {
						return nil, err
					}
					if err := emit(events.NewReasoningMessageStartEvent(thinking, "reasoning")); err != nil {
						return nil, err
					}
				}
				if err := emit(events.NewReasoningMessageContentEvent(thinking, piece)); err != nil {
					return nil, err
				}
			}
			s.thought.take(ch.Delta.JSON.ExtraFields)
			if piece := ch.Delta.Content; piece != "" {
				if err := stopThinking(); err != nil {
					return nil, err
				}
				if !started {
					if err := emit(events.NewTextMessageStartEvent(msgID, events.WithRole("assistant"))); err != nil {
						return nil, err
					}
					started = true
				}
				words.WriteString(piece)
				if err := emit(events.NewTextMessageContentEvent(msgID, piece)); err != nil {
					return nil, err
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				if err := stopThinking(); err != nil {
					return nil, err
				}
				n, seen := byIndex[tc.Index]
				if !seen {
					n = len(s.calls)
					byIndex[tc.Index] = n
					s.calls = append(s.calls, call{id: tc.ID, name: tc.Function.Name})
					if err := emit(events.NewToolCallStartEvent(tc.ID, tc.Function.Name, events.WithParentMessageID(msgID))); err != nil {
						return nil, err
					}
				}
				s.thought.takeCall(s.calls[n].id, tc.JSON.ExtraFields)
				if d := tc.Function.Arguments; d != "" {
					s.calls[n].args += d
					if err := emit(events.NewToolCallArgsEvent(s.calls[n].id, d)); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := stream.Err(); err != nil {
		return nil, emit(events.NewRunErrorEvent(err.Error(), events.WithRunID(in.RunID)))
	}
	if err := stopThinking(); err != nil {
		return nil, err
	}
	s.thought.Model = answeredBy(resp, chunkModel, a.model)
	if !s.thought.empty() {
		sealed := s.thought.seal()
		if lastThought == "" { // nothing shown: an empty reasoning message to carry it
			lastThought = events.GenerateMessageID()
			if err := emit(events.NewReasoningStartEvent(lastThought)); err != nil {
				return nil, err
			}
			if err := emit(events.NewReasoningEncryptedValueEvent(events.ReasoningEncryptedValueSubtypeMessage, lastThought, sealed)); err != nil {
				return nil, err
			}
			if err := emit(events.NewReasoningEndEvent(lastThought)); err != nil {
				return nil, err
			}
		} else if err := emit(events.NewReasoningEncryptedValueEvent(events.ReasoningEncryptedValueSubtypeMessage, lastThought, sealed)); err != nil {
			return nil, err
		}
	}
	if started {
		if err := emit(events.NewTextMessageEndEvent(msgID)); err != nil {
			return nil, err
		}
	}
	for _, c := range s.calls {
		if err := emit(events.NewToolCallEndEvent(c.id)); err != nil {
			return nil, err
		}
	}
	s.text = words.String()
	return s, nil
}

// answeredBy is who answered a call: LiteLLM names the provider and model
// it used in x-litellm-model-name, which differs from what was asked for
// after a fallback; without it, the model the chunks name, else the one
// asked for.
func answeredBy(resp *http.Response, chunkModel, asked string) string {
	if resp != nil {
		if m := resp.Header.Get("X-Litellm-Model-Name"); m != "" {
			return m
		}
	}
	if chunkModel != "" {
		return chunkModel
	}
	return asked
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
	p, err := a.Prompt(in)
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
	if err := a.Run(r.Context(), in, p, emit); err != nil {
		slog.Info("run ended early", "principal", principal, "thread", in.ThreadID, "run", in.RunID, "err", err)
	}
}
