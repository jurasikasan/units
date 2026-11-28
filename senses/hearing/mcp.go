package hearing

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer builds the MCP server exposing the open and close tools
// (C5 section 2).
func NewMCPServer(svc *Service) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "hearing", Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "open",
		Description: "Open a hearing session and get its audio_in address (\"host:port\", RTP/UDP). Optional argument recognize: list of recognizers to enable; the only value this step supports is \"speech\" (the default). Optional speech_language: the session's language (ISO 639-1, e.g. \"ru\", MRCP Speech-Language), a hint passed to STT with every utterance; empty lets STT guess. Optional, the session's own instead of the deployment's: stt_model (OpenAI's model, e.g. the router's group), stt_prompt (OpenAI's prompt), onset_ms, merge_window_ms, preroll_ms (0 is none), idle_timeout_ms (how long without a packet closes the session; 0 is never).",
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
	STTModel       string   `json:"stt_model,omitempty"`
	STTPrompt      string   `json:"stt_prompt,omitempty"`
	OnsetMS        int      `json:"onset_ms,omitempty"`
	MergeWindowMS  int      `json:"merge_window_ms,omitempty"`
	PrerollMS      *int     `json:"preroll_ms,omitempty"`
	IdleTimeoutMS  *int     `json:"idle_timeout_ms,omitempty"`
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
		o := Options{Recognize: in.Recognize, Language: in.SpeechLanguage, STTModel: in.STTModel, STTPrompt: in.STTPrompt,
			Onset: ms(in.OnsetMS), MergeWindow: ms(in.MergeWindowMS)}
		if in.PrerollMS != nil {
			p := ms(*in.PrerollMS)
			o.PreRoll = &p
		}
		if in.IdleTimeoutMS != nil {
			d := ms(*in.IdleTimeoutMS)
			o.IdleTimeout = &d
		}
		sessionID, audioIn, err := svc.OpenWith(ctx, o, traceparent(req))
		if err != nil {
			return nil, openOut{}, err
		}
		return nil, openOut{SessionID: sessionID, AudioIn: audioIn}, nil
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

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
