package body

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SelfURI is the resource a head reads the body's own description from.
const SelfURI = "body://self"

type speakIn struct {
	Text string `json:"text" jsonschema:"the words to say aloud, as they are to be said"`
}

type skipIn struct{}

// MCPServer is what a head sees of the body: its acts as tools — the list is
// the body's to declare, so a head lists them — and itself as a resource.
// speak_aloud is offered only by a body with a mouth.
func (b *Body) MCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "body", Title: "Body " + b.Cfg.Name, Version: Version}, nil)
	if b.speaking != nil {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "speak_aloud",
			Description: "Say something aloud. Whoever is within earshot hears it, as sound. " +
				"Answers once it has been said: spoken is what went out, listeners how many ears it reached (0: nobody was there to hear).",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in speakIn) (*mcp.CallToolResult, Spoken, error) {
			tp := traceparent(req)
			if strings.TrimSpace(in.Text) == "" {
				return nil, Spoken{}, errors.New("bad_argument: text is empty")
			}
			res, err := b.act(ctx, "speak_aloud", in, tp, func() (any, error) { return b.SpeakAloud(ctx, in.Text, tp) })
			s, _ := res.(Spoken)
			return nil, s, err
		})
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "skip_turn",
		Description: "Do nothing this turn, on purpose: wait for what comes next.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in skipIn) (*mcp.CallToolResult, Skipped, error) {
		_, err := b.act(ctx, "skip_turn", in, traceparent(req), func() (any, error) { return Skipped{Skipped: true}, nil })
		return nil, Skipped{Skipped: true}, err
	})
	srv.AddResource(&mcp.Resource{
		URI: SelfURI, Name: "self", MIMEType: "application/json",
		Description: "This body as an observer perceives it, and where its senses take input.",
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		raw, err := json.Marshal(b.Self())
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: SelfURI, MIMEType: "application/json", Text: string(raw)}}}, nil
	})
	return srv
}

// mcpHandler serves the body's MCP: streamable HTTP, stateless, JSON responses.
func (b *Body) mcpHandler() http.Handler {
	srv := b.MCPServer()
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// traceparent reads the W3C traceparent of an MCP call from its _meta.
func traceparent(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	tp, _ := req.Params.Meta["traceparent"].(string)
	return tp
}
