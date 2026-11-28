package sight

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer builds the MCP server exposing the open and close tools
// (CONTRACT §2).
func NewMCPServer(svc *Service, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "sight", Version: version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "open",
		Description: "Open a sight session and get its video_in (\"host:port\", UDP): send RTP video there, H.264 (RFC 6184, " +
			"packetization mode 0 or 1, any dynamic payload type, SPS/PPS in band), RTCP sender reports to the port above; " +
			"and its sight_in URL: POST SenML packs (RFC 8428, application/senml+json) there, " +
			"records named description or read (vs: words) or frame (vd: a single JPEG or PNG, base64url), each with its time t. " +
			"Optional: recognize (only \"scene\", the default); language (ISO 639-1) to describe in; " +
			"the session's own instead of the deployment's: vision_model (OpenAI's model), vision_prompt, " +
			"describe_every_ms (at most one description per this much of the input's time), change_threshold (0–1), " +
			"idle_timeout_ms (how long without input closes the session; 0 is never).",
	}, openTool(svc))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "close",
		Description: "Close a sight session: video_in stops and what it got is looked at, a change still waiting for its words is described, then session-closed; sight_in stops taking packs.",
	}, closeTool(svc))
	return srv
}

type openIn struct {
	Recognize       []string `json:"recognize,omitempty"`
	Language        string   `json:"language,omitempty"`
	VisionModel     string   `json:"vision_model,omitempty"`
	VisionPrompt    string   `json:"vision_prompt,omitempty"`
	DescribeEveryMS int      `json:"describe_every_ms,omitempty"`
	ChangeThreshold float64  `json:"change_threshold,omitempty"`
	IdleTimeoutMS   *int     `json:"idle_timeout_ms,omitempty"`
}

type openOut struct {
	SessionID string `json:"session_id"`
	VideoIn   string `json:"video_in"`
	SightIn   string `json:"sight_in"`
}

type closeIn struct {
	SessionID string `json:"session_id"`
}

type closeOut struct {
	OK bool `json:"ok"`
}

func openTool(svc *Service) mcp.ToolHandlerFor[openIn, openOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in openIn) (*mcp.CallToolResult, openOut, error) {
		opts := Options{
			Recognize:       in.Recognize,
			Language:        in.Language,
			VisionModel:     in.VisionModel,
			VisionPrompt:    in.VisionPrompt,
			DescribeEvery:   time.Duration(in.DescribeEveryMS) * time.Millisecond,
			ChangeThreshold: in.ChangeThreshold,
		}
		if in.IdleTimeoutMS != nil {
			d := time.Duration(*in.IdleTimeoutMS) * time.Millisecond
			opts.IdleTimeout = &d
		}
		o, err := svc.Open(ctx, opts, traceparent(req))
		if err != nil {
			return nil, openOut{}, err
		}
		return nil, openOut{SessionID: o.SessionID, VideoIn: o.VideoIn, SightIn: o.SightIn}, nil
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
	return func(_ context.Context, _ *mcp.CallToolRequest, in closeIn) (*mcp.CallToolResult, closeOut, error) {
		if err := svc.Close(in.SessionID); err != nil {
			return nil, closeOut{}, err
		}
		return nil, closeOut{OK: true}, nil
	}
}
