package driver

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/client/sse"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/sirupsen/logrus"
)

// Mind runs a prompt and says what came of it: the pieces as they come,
// the reply when it is whole.
type Mind interface {
	// Run asks with the messages and the model; piece is called with each
	// piece of the answer as it comes ("text" or "reasoning"). The answer is
	// the reply as threads' blocks, in the order it was made: each text
	// message that said any words, each tool call that was answered and
	// its result (reply.go). A run that ends in an error is *RunError.
	Run(ctx context.Context, thread, run string, messages []types.Message, model string, piece func(part, text string)) ([]Block, error)
}

// RunError is the mind's RUN_ERROR, or why no run came back at all.
type RunError struct {
	Code    string
	Message string
}

func (e *RunError) Error() string { return e.Message }

// AGUI is a mind behind AG-UI (minds/assistant's POST /run), spoken to
// with the protocol's own Go SDK: its client, its events.
type AGUI struct {
	client *sse.Client
	// Principal is who the driver is to the mind: it is reached inside the
	// stack, not through the door, and names itself.
	Principal string
}

func NewAGUI(runURL, principal string) *AGUI {
	quiet := logrus.New()
	quiet.SetOutput(io.Discard)
	return &AGUI{
		client:    sse.NewClient(sse.Config{Endpoint: runURL, ReadTimeout: 10 * time.Minute, Logger: quiet}),
		Principal: principal,
	}
}

func (a *AGUI) Run(ctx context.Context, thread, run string, messages []types.Message, model string, piece func(part, text string)) ([]Block, error) {
	in := types.RunAgentInput{
		ThreadID: thread,
		RunID:    run,
		State:    map[string]any{},
		Messages: messages,
		Tools:    []types.Tool{},
		Context:  []types.Context{},
	}
	if model != "" {
		in.ForwardedProps = map[string]any{"modelName": model}
	} else {
		in.ForwardedProps = map[string]any{}
	}
	frames, errs, err := a.client.Stream(sse.StreamOptions{Context: ctx, Payload: in, Headers: map[string]string{"X-Principal-Id": a.Principal}})
	if err != nil {
		return nil, &RunError{Code: "mind_unreachable", Message: "The mind did not answer: " + err.Error()}
	}
	dec := events.NewEventDecoder(quietLogger())
	r := newReply()
	for f := range frames {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(f.Data, &head) != nil {
			continue
		}
		ev, err := dec.DecodeEvent(head.Type, f.Data)
		if err != nil {
			continue // a kind this SDK does not know: not one the driver reads
		}
		switch e := ev.(type) {
		case *events.TextMessageStartEvent:
			r.start(e.MessageID)
		case *events.TextMessageContentEvent:
			r.words(e.MessageID, e.Delta)
			piece("text", e.Delta)
		case *events.ToolCallStartEvent:
			r.call(e.ToolCallID, e.ToolCallName)
		case *events.ToolCallArgsEvent:
			r.args(e.ToolCallID, e.Delta)
		case *events.ToolCallResultEvent:
			var raw any
			if e.BaseEvent != nil {
				raw = e.RawEvent
			}
			r.result(e.ToolCallID, e.Content, raw)
		case *events.ReasoningMessageContentEvent:
			piece("reasoning", e.Delta)
		case *events.RunErrorEvent:
			code := ""
			if e.Code != nil {
				code = *e.Code
			}
			return nil, &RunError{Code: code, Message: e.Message}
		case *events.RunFinishedEvent:
			return r.blocks(), nil
		}
	}
	if err := <-errs; err != nil {
		return nil, &RunError{Code: "mind_failed", Message: "The mind's answer broke off: " + err.Error()}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, &RunError{Code: "mind_failed", Message: "The mind's answer ended without RUN_FINISHED"}
}

func quietLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}
