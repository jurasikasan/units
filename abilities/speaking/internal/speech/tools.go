package speech

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type openArgs struct {
	To        string  `json:"to"`
	VoiceName string  `json:"voice_name"`
	Codec     *string `json:"codec,omitempty"`
}

type openResult struct {
	SessionID string `json:"session_id"`
}

type speakArgs struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type speakResult struct {
	RequestID string `json:"request_id"`
}

type sessionArgs struct {
	SessionID string `json:"session_id"`
}

type okResult struct {
	OK bool `json:"ok"`
}

// Server builds the mcp.Server with the open / speak / stop / barge_in_occurred /
// close tools (C4 §1–2; MRCPv2's synthesizer verbs, RFC 6787 §8).
// A tool error is an MCP tool error (isError: true) whose text starts with
// the code from C4 §2.5.
func Server(sp *Speech) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "speech",
		Title:   "Speech",
		Version: Version,
	}, nil)

	mcp.AddTool[openArgs, openResult](srv, &mcp.Tool{
		Name: "open",
		Description: "Open a speaking session: from now on, audio is sent to to " +
			"(UDP host:port) in voice_name (MRCP Voice-Name). Codec is optional; L16 (48 kHz PCM) is the only one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in openArgs) (*mcp.CallToolResult, openResult, error) {
		codec := ""
		if in.Codec != nil {
			codec = *in.Codec
		}
		id, err := sp.Open(ctx, in.To, in.VoiceName, codec, traceparent(req))
		if err != nil {
			return nil, openResult{}, err
		}
		return nil, openResult{SessionID: id}, nil
	})

	mcp.AddTool[speakArgs, speakResult](srv, &mcp.Tool{
		Name:        "speak",
		Description: "SPEAK: speak text on an open session. Requests play in the order they were made; every request gets exactly one speak-complete event.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in speakArgs) (*mcp.CallToolResult, speakResult, error) {
		rid, err := sp.Speak(in.SessionID, in.Text)
		if err != nil {
			return nil, speakResult{}, err
		}
		return nil, speakResult{RequestID: rid}, nil
	})

	mcp.AddTool[sessionArgs, okResult](srv, &mcp.Tool{
		Name:        "stop",
		Description: "STOP: what is playing is cut, what is queued is dropped, the session stays. Each affected request gets its speak-complete (cause cancelled) before this returns.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionArgs) (*mcp.CallToolResult, okResult, error) {
		if err := sp.Stop(in.SessionID); err != nil {
			return nil, okResult{}, err
		}
		return nil, okResult{OK: true}, nil
	})

	mcp.AddTool[sessionArgs, okResult](srv, &mcp.Tool{
		Name:        "barge_in_occurred",
		Description: "BARGE-IN-OCCURRED: the other side started talking. The same cut as stop; each affected request gets its speak-complete (cause barge-in) before this returns.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionArgs) (*mcp.CallToolResult, okResult, error) {
		if err := sp.BargeInOccurred(in.SessionID); err != nil {
			return nil, okResult{}, err
		}
		return nil, okResult{OK: true}, nil
	})

	mcp.AddTool[sessionArgs, okResult](srv, &mcp.Tool{
		Name:        "close",
		Description: "End an open session: what is playing is cut, what is queued is dropped. Closing a closed session is ok.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionArgs) (*mcp.CallToolResult, okResult, error) {
		if err := sp.Close(in.SessionID); err != nil {
			return nil, okResult{}, err
		}
		return nil, okResult{OK: true}, nil
	})

	return srv
}

// traceparent is the W3C traceparent of the open call, from the MCP call's
// metadata (C0 §5). Empty when the call carries none: then the session's
// events have no ce_traceparent at all, never a made-up value.
func traceparent(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	tp, _ := req.Params.Meta["traceparent"].(string)
	return tp
}
