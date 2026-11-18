package assistant

import (
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/openai/openai-go/v3"
)

// What a model call weighs: every call asks for usage
// (stream_options.include_usage), which comes in the stream's last chunk.
// Its prompt tokens are the weight of the context as the model read it:
// the system prompt, the whole conversation, attachments, the tools'
// schemas. It is kept with the turn (said.usage), in AG-UI's own shape
// (TokenUsage, numbers only, by provider and model), and goes out three
// ways:
//
//   - in the metadata of the event that ends the turn (its last
//     TOOL_CALL_END, or TEXT_MESSAGE_END when it calls no tool), under
//     "usage": the weight at that answer;
//   - in RUN_FINISHED.usage, or RUN_ERROR.usage, summed over the run's
//     calls, one entry per provider and model;
//   - in the log, a line per call.
//
// A model that tells nothing leaves nothing: no zeros made up.

// UsageKey is the metadata key the weight of a turn's call goes under.
const UsageKey = "usage"

// callUsage is a call's usage as AG-UI shapes it; nil when the stream
// carried none. model is who answered, "provider/model" as LiteLLM names it.
func callUsage(u openai.CompletionUsage, seen bool, model string) *events.TokenUsage {
	if !seen {
		return nil
	}
	tu := &events.TokenUsage{
		InputTokens:  events.TokenCount(u.PromptTokens),
		OutputTokens: events.TokenCount(u.CompletionTokens),
		TotalTokens:  events.TokenCount(u.TotalTokens),
	}
	tu.Provider, tu.Model = splitModel(model)
	if u.CompletionTokensDetails.JSON.ReasoningTokens.Valid() {
		tu.ReasoningTokens = events.TokenCount(u.CompletionTokensDetails.ReasoningTokens)
	}
	if u.PromptTokensDetails.JSON.CachedTokens.Valid() {
		tu.CachedInputTokens = events.TokenCount(u.PromptTokensDetails.CachedTokens)
	}
	return tu
}

// splitModel is LiteLLM's "provider/model" as AG-UI's two labels; a name
// with no provider is all model.
func splitModel(name string) (provider, model string) {
	if p, m, ok := strings.Cut(name, "/"); ok {
		return p, m
	}
	return "", name
}

// tally is a run's usage, summed by provider and model in the order they
// first answered.
type tally []events.TokenUsage

func (t *tally) add(u *events.TokenUsage) {
	if u == nil {
		return
	}
	for i := range *t {
		e := &(*t)[i]
		if e.Provider == u.Provider && e.Model == u.Model {
			e.InputTokens = sum(e.InputTokens, u.InputTokens)
			e.OutputTokens = sum(e.OutputTokens, u.OutputTokens)
			e.TotalTokens = sum(e.TotalTokens, u.TotalTokens)
			e.ReasoningTokens = sum(e.ReasoningTokens, u.ReasoningTokens)
			e.CachedInputTokens = sum(e.CachedInputTokens, u.CachedInputTokens)
			return
		}
	}
	*t = append(*t, *u)
}

// sum adds two counts, either of which may be missing.
func sum(a, b *int64) *int64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return events.TokenCount(*a + *b)
}

// withUsage puts a call's usage into an event's metadata.
func withUsage(ev events.Event, u *events.TokenUsage) events.Event {
	if u != nil {
		b := ev.GetBaseEvent()
		b.Metadata = types.MergeMetadata(b.Metadata, types.Metadata{UsageKey: *u})
	}
	return ev
}

// count is a count for the log: -1 when not told.
func count(n *int64) int64 {
	if n == nil {
		return -1
	}
	return *n
}
