package hearing

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer builds the MCP server exposing the open and close tools
// (C5 section 2).
func NewMCPServer(svc *Service) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "hearing", Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "open",
		Description: "Open a hearing session and get its audio_in address (\"host:port\", RTP/UDP). Optional argument recognize: list of recognizers to enable; the only value this step supports is \"speech\" (the default). Optional speech_language: the session's language (ISO 639-1, e.g. \"ru\", MRCP Speech-Language), a hint passed to STT with every utterance; empty lets STT guess.",
	}, openTool(svc))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "close",
		Description: "Close a hearing session: it stops listening, ends any in-progress utterance and frees audio_in.",
	}, closeTool(svc))
	return srv
}

type openIn struct {
	Recognize      []string `json:"recognize,omitempty"`
	SpeechLanguage string   `json:"speech_language,omitempty"`
}

type openOut struct {
	SessionID string `json:"session_id"`
	AudioIn   string `json:"audio_in"`
}

type closeIn struct {
	SessionID string `json:"session_id"`
}

type closeOut struct {
	OK bool `json:"ok"`
}

func openTool(svc *Service) mcp.ToolHandlerFor[openIn, openOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in openIn) (*mcp.CallToolResult, openOut, error) {
		sessionID, audioIn, err := svc.Open(ctx, in.Recognize, in.SpeechLanguage, traceparent(req))
		if err != nil {
			return nil, openOut{}, err
		}
		return nil, openOut{SessionID: sessionID, AudioIn: audioIn}, nil
	}
}

// traceparent reads the W3C traceparent of the MCP call from its _meta
// (contracts/events.md); an absent or non-string value is an empty string.
func traceparent(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	tp, _ := req.Params.Meta["traceparent"].(string)
	return tp
}

func closeTool(svc *Service) mcp.ToolHandlerFor[closeIn, closeOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in closeIn) (*mcp.CallToolResult, closeOut, error) {
		if err := svc.Close(in.SessionID); err != nil {
			return nil, closeOut{}, err
		}
		return nil, closeOut{OK: true}, nil
	}
}
