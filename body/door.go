package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Door is an MCP client to one unit (a sense, an ability), spoken through the
// official Go SDK over streamable HTTP. One client session is kept and used
// for every call; a call that fails below the tool (the unit restarted, the
// session is gone) drops it, and the next call connects again.
type Door struct {
	Name string // for errors and logs: hearing, sight, speaking
	URL  string
	HTTP *http.Client

	mu sync.Mutex
	cs *mcp.ClientSession
}

// ToolError is a tool's own answer that it failed (isError), its text
// starting with the unit's error code (senses' and speaking's §2).
type ToolError struct{ Text string }

func (e *ToolError) Error() string { return e.Text }

// Code is the error's leading code: "unknown_session: …" → unknown_session.
func (e *ToolError) Code() string {
	code, _, _ := strings.Cut(e.Text, ":")
	return strings.TrimSpace(code)
}

// IsCode reports whether err is a tool error with that code.
func IsCode(err error, code string) bool {
	var te *ToolError
	return errors.As(err, &te) && te.Code() == code
}

func (d *Door) session(ctx context.Context) (*mcp.ClientSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cs != nil {
		return d.cs, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "body", Version: Version}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: d.URL, HTTPClient: d.HTTP, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: connect: %w", d.Name, err)
	}
	d.cs = cs
	return cs, nil
}

func (d *Door) drop(cs *mcp.ClientSession) {
	d.mu.Lock()
	if d.cs == cs {
		d.cs = nil
	}
	d.mu.Unlock()
	_ = cs.Close()
}

// Call calls tool with args and reads its structured answer into out. A
// tool's own failure is a *ToolError; anything else is the door's.
func (d *Door) Call(ctx context.Context, tool string, args any, traceparent string, out any) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	cs, err := d.session(ctx)
	if err != nil {
		return err
	}
	params := &mcp.CallToolParams{Name: tool, Arguments: args}
	if traceparent != "" {
		params.Meta = mcp.Meta{"traceparent": traceparent}
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		d.drop(cs)
		return fmt.Errorf("%s %s: %w", d.Name, tool, err)
	}
	if res.IsError {
		return &ToolError{Text: text(res)}
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(res.StructuredContent)
	if res.StructuredContent == nil || err != nil {
		raw = []byte(text(res)) // a unit that answers its JSON as text only
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: answer not understood: %w", d.Name, tool, err)
	}
	return nil
}

// Close ends the session, if there is one.
func (d *Door) Close() {
	d.mu.Lock()
	cs := d.cs
	d.cs = nil
	d.mu.Unlock()
	if cs != nil {
		_ = cs.Close()
	}
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// detached keeps ctx's cancellation and deadline and drops its values. A call
// out of the body is often made inside a head's MCP request, and go-sdk's
// server puts that request's protocol version into its context, which its
// client then sends on as Mcp-Protocol-Version: the head's version, which a
// unit on an older SDK refuses (400 on initialize). What the body says to a
// sense or an ability is its own conversation, not the head's.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	out, cancel := context.WithCancel(context.Background())
	if dl, ok := ctx.Deadline(); ok {
		var cancelDl context.CancelFunc
		out, cancelDl = context.WithDeadline(out, dl)
		prev := cancel
		cancel = func() { cancelDl(); prev() }
	}
	stop := context.AfterFunc(ctx, cancel)
	return out, func() { stop(); cancel() }
}
