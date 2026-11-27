package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// Body is the brain's way to act: a body's MCP (body/, BRAIN_BODY), spoken
// through the official Go SDK. The brain knows the body only by the tools it
// lists — never assumes them — and by what each call answers. One client
// session is kept; a failure below the tool drops it, and the next call
// connects again.
type Body struct {
	URL  string
	HTTP *http.Client

	mu sync.Mutex
	cs *mcp.ClientSession
}

func (b *Body) session(ctx context.Context) (*mcp.ClientSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cs != nil {
		return b.cs, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "brain", Version: "0.1.0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: b.URL, HTTPClient: b.HTTP, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("body: connect: %w", err)
	}
	b.cs = cs
	return cs, nil
}

func (b *Body) drop(cs *mcp.ClientSession) {
	b.mu.Lock()
	if b.cs == cs {
		b.cs = nil
	}
	b.mu.Unlock()
	_ = cs.Close()
}

// Tools lists the body's tools as OpenAI function tools: the name, the
// description and the input schema as the body declares them.
func (b *Body) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	cs, err := b.session(ctx)
	if err != nil {
		return nil, err
	}
	var out []openai.ChatCompletionToolUnionParam
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			b.drop(cs)
			return nil, fmt.Errorf("body: tools/list: %w", err)
		}
		fn := shared.FunctionDefinitionParam{Name: tool.Name}
		if tool.Description != "" {
			fn.Description = openai.String(tool.Description)
		}
		var params shared.FunctionParameters
		if raw, err := json.Marshal(tool.InputSchema); err == nil && json.Unmarshal(raw, &params) == nil && params != nil {
			fn.Parameters = params
		} else {
			fn.Parameters = shared.FunctionParameters{"type": "object"}
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out, nil
}

// Act calls one tool with args (a JSON object) and gives what the body
// answered in words: its text content. isError is the tool's own failure
// (the body said so); err is a call that did not reach the tool.
func (b *Body) Act(ctx context.Context, name string, args map[string]any) (result string, isError bool, err error) {
	cs, err := b.session(ctx)
	if err != nil {
		return "", false, err
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		b.drop(cs)
		return "", false, fmt.Errorf("body: %s: %w", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(raw))
		}
	}
	return strings.Join(parts, "\n"), res.IsError, nil
}

// Close ends the session to the body, if there is one.
func (b *Body) Close() {
	b.mu.Lock()
	cs := b.cs
	b.cs = nil
	b.mu.Unlock()
	if cs != nil {
		_ = cs.Close()
	}
}
