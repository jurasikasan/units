package assistant

import (
	"log/slog"
	"regexp"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// theirTools is what the client offers for one run (RunAgentInput.tools):
// tools the model may call and the client makes, as AG-UI has it. The
// mind shows them to the model beside its own and never calls them; a
// call to one ends the run, and the client's next run brings the result.
type theirTools struct {
	tools []openai.ChatCompletionToolUnionParam
	names map[string]bool
}

// fnName is what OpenAI takes as a function's name.
var fnName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// offered is the client's tools as the model is shown them, their
// parameters as they came (an object with no properties when none). A
// name the model cannot take, or one taken already (by a tool of the
// mind's, or by an earlier one of the client's), is logged and left out:
// one bad tool would otherwise fail every call of the run.
func offered(in []types.Tool, k *kit) theirTools {
	t := theirTools{names: map[string]bool{}}
	for _, tool := range in {
		switch {
		case !fnName.MatchString(tool.Name):
			slog.Warn("a client's tool has a name a model cannot take; left out", "name", tool.Name)
			continue
		case k.has(tool.Name) || t.names[tool.Name]:
			slog.Warn("a client's tool has a name taken already; left out", "name", tool.Name)
			continue
		}
		t.names[tool.Name] = true
		params := shared.FunctionParameters{"type": "object", "properties": map[string]any{}}
		if tool.Parameters != nil {
			params = schema(tool.Parameters)
		}
		t.tools = append(t.tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        tool.Name,
			Description: openai.String(tool.Description),
			Parameters:  params,
		}))
	}
	return t
}

// split is a turn's calls the mind makes, in their order, and whether any
// is left to the client.
func (t theirTools) split(calls []call) (ours []call, left bool) {
	for _, c := range calls {
		if t.names[c.name] {
			left = true
			continue
		}
		ours = append(ours, c)
	}
	return ours, left
}

// has says whether the mind has a tool by this name.
func (k *kit) has(name string) bool {
	if k == nil {
		return false
	}
	_, ok := k.route[name]
	return ok
}

// offer is the mind's own tools as the model is shown them; none without a
// kit.
func (k *kit) offer() []openai.ChatCompletionToolUnionParam {
	if k == nil {
		return nil
	}
	return k.tools
}
