// Package mcpserver offers phone's tools over MCP (phone/CONTRACT.md §2).
// Each returns at once; what follows comes as events.
package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/call"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/device"
)

type DialIn struct {
	To           string `json:"to" jsonschema:"the number to call, E.164: + and digits"`
	From         string `json:"from,omitempty" jsonschema:"caller ID, passed on as given; the trunk's own if left out"`
	RingTimeoutS int    `json:"ring_timeout_s,omitempty" jsonschema:"how long to let it ring, seconds; the server's default if left out, capped by its maximum"`
}

type DialOut struct {
	CallID string `json:"call_id"`
}

type CallIn struct {
	CallID string `json:"call_id"`
}

type HangupOut struct {
	OK bool `json:"ok"`
}

type ListenIn struct {
	CallID string `json:"call_id"`
	To     string `json:"to" jsonschema:"where to send the far end's audio: host:port over UDP; empty stops it"`
}

type ListenOut struct {
	OK bool `json:"ok"`
}

func New(d *device.Device, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "phone", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name: "dial",
		Description: "Start a phone call and return its call_id before anyone answers. " +
			"What happens next comes as events on phone_events: ringing, answered (with the audio_in " +
			"socket to feed its audio), ended with a reason.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in DialIn) (*mcp.CallToolResult, DialOut, error) {
		id, err := d.Dial(call.DialRequest{
			To: in.To, From: in.From, RingTimeout: time.Duration(in.RingTimeoutS) * time.Second,
			Traceparent: traceparent(req),
		})
		if r := refusal(err); r != nil {
			return r, DialOut{}, nil
		}
		return nil, DialOut{CallID: id}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "answer",
		Description: "Take a ringing inbound call: phone answers it (200 OK) and its audio flows on the call as on an outbound one. " +
			"Refusals: unknown_call (no such call_id), not_ringing (the call is outbound, already answered, or already ended).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in CallIn) (*mcp.CallToolResult, HangupOut, error) {
		if r := refusal(d.Answer(in.CallID)); r != nil {
			return r, HangupOut{}, nil
		}
		return nil, HangupOut{OK: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "listen",
		Description: "Start sending the far end's audio of an answered call to to (host:port, UDP) as RTP: " +
			"its G.711 (PCMU or PCMA, as it arrives from the trunk, no transcoding), 20 ms packets, the " +
			"call's own sequence, timestamp and one SSRC per call. One destination per call: a second " +
			"listen moves the stream to the new address; to:\"\" stops it. Sending stops with the call. " +
			"Refusals: unknown_call, not_answered (before answered), bad_target (to not a resolvable host:port).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ListenIn) (*mcp.CallToolResult, ListenOut, error) {
		if r := refusal(d.Listen(in.CallID, in.To)); r != nil {
			return r, ListenOut{}, nil
		}
		return nil, ListenOut{OK: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hangup",
		Description: "End a call: cancel it while it rings, hang up once answered. A call that already ended is not an error.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in CallIn) (*mcp.CallToolResult, HangupOut, error) {
		d.Hangup(in.CallID)
		return nil, HangupOut{OK: true}, nil
	})
	return s
}

// refusal turns a contract error into a tool error whose text starts with its
// code (CONTRACT.md §2.5). Anything else stays a Go error.
func refusal(err error) *mcp.CallToolResult {
	var e *call.Error
	if !errors.As(err, &e) {
		return nil
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}
}

func traceparent(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	tp, _ := req.Params.Meta["traceparent"].(string)
	return tp
}
