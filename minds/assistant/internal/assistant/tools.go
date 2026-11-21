package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// Server is an MCP server whose tools the model may call: a name of ours
// and its Streamable HTTP endpoint.
type Server struct {
	Name string
	URL  string
}

// ParseServers reads "name=url,name=url" (spaces around items allowed).
func ParseServers(s string) ([]Server, error) {
	var out []Server
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, url, ok := strings.Cut(item, "=")
		if !ok || name == "" || url == "" {
			return nil, fmt.Errorf("an MCP server is name=url, not %q", item)
		}
		out = append(out, Server{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
	}
	return out, nil
}

// kit is the tools of one run: a session per server that answered, and the
// tools as the model is shown them. MCP 2026-07-28 is stateless, so a run
// opening its own sessions costs a discovery call per server, not a
// handshake kept alive.
type kit struct {
	sessions []*mcp.ClientSession
	tools    []openai.ChatCompletionToolUnionParam
	route    map[string]route
}

type route struct {
	session *mcp.ClientSession
	tool    string
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// toolName is how the model sees a server's tool: server__tool, in the
// characters and length OpenAI allows for a function name.
func toolName(server, tool string) string {
	n := unsafeName.ReplaceAllString(server+"__"+tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// openKit connects to every server and lists its tools. A server that does
// not answer is logged and left out: the run goes on without its tools.
func openKit(ctx context.Context, client *mcp.Client, servers []Server) *kit {
	k := &kit{route: map[string]route{}}
	for _, s := range servers {
		cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.URL, DisableStandaloneSSE: true}, nil)
		if err != nil {
			slog.Warn("an MCP server did not answer; its tools are left out", "server", s.Name, "err", err)
			continue
		}
		k.sessions = append(k.sessions, cs)
		for t, err := range cs.Tools(ctx, nil) {
			if err != nil {
				slog.Warn("listing an MCP server's tools failed", "server", s.Name, "err", err)
				break
			}
			name := toolName(s.Name, t.Name)
			if _, taken := k.route[name]; taken {
				slog.Warn("two tools under one name; the second is left out", "name", name)
				continue
			}
			k.route[name] = route{session: cs, tool: t.Name}
			k.tools = append(k.tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        name,
				Description: openai.String(t.Description),
				Parameters:  schema(t.InputSchema),
			}))
		}
	}
	return k
}

// schema is a tool's input schema as OpenAI's function parameters: the
// same JSON Schema, carried over as a map.
func schema(in any) shared.FunctionParameters {
	b, err := json.Marshal(in)
	if err != nil {
		return shared.FunctionParameters{"type": "object"}
	}
	var out shared.FunctionParameters
	if json.Unmarshal(b, &out) != nil || out == nil {
		return shared.FunctionParameters{"type": "object"}
	}
	return out
}

func (k *kit) close() {
	for _, cs := range k.sessions {
		_ = cs.Close()
	}
}

// answer is what came of a tool call: the words the model reads back, and
// the tool's own result as MCP gave it (nil when no tool answered: no such
// tool, arguments that are not JSON, a server gone). The result is the
// tool's, pictures included; it goes on to whoever listens to the run.
type answer struct {
	words  string
	result *mcp.CallToolResult
}

// call runs one tool call as the model made it and says what came of it,
// in words the model reads back. A call that cannot be made, or a tool that
// reports an error, is an answer too: the model hears it and goes on.
func (k *kit) call(ctx context.Context, name, args string) answer {
	r, ok := k.route[name]
	if !ok {
		return answer{words: fmt.Sprintf("there is no tool %q", name)}
	}
	var a map[string]any
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return answer{words: "the arguments are not a JSON object: " + err.Error()}
		}
	}
	res, err := r.session.CallTool(ctx, &mcp.CallToolParams{Name: r.tool, Arguments: a})
	if err != nil {
		return answer{words: "the tool could not be called: " + err.Error()}
	}
	return answer{words: words(res), result: res}
}

// words is a tool's result as text for the model. Text is passed as it is;
// structured content, when there is no text, as JSON; an image, a sound or
// a binary resource is named, not carried: the model reads a tool's
// picture as words (the picture itself goes to the run's listener, in
// TOOL_CALL_RESULT's rawEvent).
func words(res *mcp.CallToolResult) string {
	var out []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			out = append(out, c.Text)
		case *mcp.ImageContent:
			out = append(out, fmt.Sprintf("[an image, %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.AudioContent:
			out = append(out, fmt.Sprintf("[a sound, %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.ResourceLink:
			out = append(out, fmt.Sprintf("[a resource: %s %s]", c.Name, c.URI))
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				out = append(out, c.Resource.Text)
			} else if c.Resource != nil {
				out = append(out, fmt.Sprintf("[a resource: %s, %s]", c.Resource.URI, c.Resource.MIMEType))
			}
		}
	}
	if len(out) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			out = append(out, string(b))
		}
	}
	return strings.Join(out, "\n")
}

// toolResult is a call's TOOL_CALL_RESULT. AG-UI has no event or part for
// media an agent's tool makes, and its content is a string: content is the
// words the model read back, as any AG-UI client shows them, and rawEvent
// is the tool's result as MCP gave it (CallToolResult: its content blocks,
// an image's base64 bytes and mimeType among them, isError). Without a
// tool's answer there is no rawEvent (contracts/edges.md).
func toolResult(callID string, a answer) events.Event {
	e := events.NewToolCallResultEvent(events.GenerateMessageID(), callID, a.words)
	if a.result != nil {
		e.RawEvent = a.result
	}
	return e
}

// callAll makes a turn's calls at once and returns what each said, in the
// order the model made them. With no kit every call is answered as unknown.
func (k *kit) callAll(ctx context.Context, calls []call) []answer {
	out := make([]answer, len(calls))
	done := make(chan struct{}, len(calls))
	for i, c := range calls {
		go func() {
			defer func() { done <- struct{}{} }()
			if k == nil {
				out[i] = answer{words: fmt.Sprintf("there is no tool %q", c.name)}
				return
			}
			out[i] = k.call(ctx, c.name, c.args)
		}()
	}
	for range calls {
		<-done
	}
	return out
}
