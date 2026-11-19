// Package organs reaches hearing and speaking the way any driver does: their
// tools over MCP, their events from Redis streams (contracts/events.md).
package organs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Organ is one MCP server, connected on first use and again after a
// failure (an organ restarted, its session gone).
type Organ struct {
	name, url, version string

	mu   sync.Mutex
	sess *mcp.ClientSession
}

// New names the organ (for errors) and its MCP endpoint.
func New(name, url, version string) *Organ { return &Organ{name: name, url: url, version: version} }

func (o *Organ) session(ctx context.Context) (*mcp.ClientSession, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sess != nil {
		return o.sess, nil
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "minds/realtime", Version: o.version}, nil)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: o.url}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.name, err)
	}
	o.sess = s
	return s, nil
}

func (o *Organ) drop(s *mcp.ClientSession) {
	o.mu.Lock()
	if o.sess == s {
		o.sess = nil
	}
	o.mu.Unlock()
	_ = s.Close()
}

// ToolError is a tool's own refusal: its text starts with the organ's code
// (capacity, unknown_session, …).
type ToolError struct{ Organ, Tool, Text string }

func (e *ToolError) Error() string { return fmt.Sprintf("%s %s: %s", e.Organ, e.Tool, e.Text) }

// Code is the refusal's code, the text's first word.
func (e *ToolError) Code() string {
	code, _, _ := strings.Cut(e.Text, ":")
	return code
}

// Call calls a tool and reads its structured result into out (nil: thrown
// away). A failure to reach the organ is tried once more on a new
// connection; a tool's refusal is a *ToolError.
func (o *Organ) Call(ctx context.Context, tool string, args map[string]any, out any) error {
	var res *mcp.CallToolResult
	for attempt := 0; ; attempt++ {
		s, err := o.session(ctx)
		if err != nil {
			return err
		}
		res, err = s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err == nil {
			break
		}
		o.drop(s)
		if attempt > 0 || ctx.Err() != nil {
			return fmt.Errorf("%s %s: %w", o.name, tool, err)
		}
	}
	if res.IsError {
		var text []string
		for _, c := range res.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				text = append(text, t.Text)
			}
		}
		return &ToolError{Organ: o.name, Tool: tool, Text: strings.Join(text, " ")}
	}
	if out == nil {
		return nil
	}
	if res.StructuredContent == nil {
		return errors.New(o.name + " " + tool + ": no structured result")
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
