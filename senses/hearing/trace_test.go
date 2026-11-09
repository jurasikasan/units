package hearing

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The traceparent values come from the task, not from this code.
const (
	tpOne   = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tpOther = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-02"
)

func mcpCall(t *testing.T, cs *mcp.ClientSession, name string, args any, meta mcp.Meta) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args, Meta: meta})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s is an error: %q", name, toolText(t, res))
	}
	return res
}

func mcpOpen(t *testing.T, cs *mcp.ClientSession, meta mcp.Meta) (string, string) {
	t.Helper()
	res := mcpCall(t, cs, "open", nil, meta)
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %#v", res.StructuredContent)
	}
	sid, _ := sc["session_id"].(string)
	addr, _ := sc["audio_in"].(string)
	if sid == "" || addr == "" {
		t.Fatalf("open out = %#v, want session_id and audio_in", sc)
	}
	return sid, addr
}

func mcpClose(t *testing.T, cs *mcp.ClientSession, sid string) {
	t.Helper()
	mcpCall(t, cs, "close", map[string]any{"session_id": sid}, nil)
}

// eventsOf is a snapshot of one session's events, in arrival order.
func eventsOf(h *harness, sid string) []Event {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	var out []Event
	for _, ev := range h.sink.Events {
		if ev.Subject == sid {
			out = append(out, ev)
		}
	}
	return out
}

// waitSession blocks until the session has at least n events.
func waitSession(t *testing.T, h *harness, sid string, n int) []Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs := eventsOf(h, sid)
		if len(evs) >= n {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d events of session %s; has %d: %+v", n, sid, len(evs), evs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// driveUtterance sends a 1 s burst followed by enough silence for the
// merge window, and waits for the session's started and heard events.
func driveUtterance(t *testing.T, h *harness, sid, addr string) {
	t.Helper()
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 1.0)
	waitSession(t, h, sid, 2) // speech.started + utterance.heard
}

// finish closes the session over MCP and returns its full event list:
// speech.started, utterance.heard, session.closed, in that order.
func finish(t *testing.T, h *harness, cs *mcp.ClientSession, sid string) []Event {
	t.Helper()
	mcpClose(t, cs, sid)
	evs := waitSession(t, h, sid, 3)
	want := []string{"speechrecog.start-of-input", "speechrecog.recognition-complete", "speechrecog.session-closed"}
	if len(evs) != len(want) {
		t.Fatalf("session %s: %d events, want %d: %+v", sid, len(evs), len(want), evs)
	}
	for i, typ := range want {
		if evs[i].Type != typ {
			t.Fatalf("session %s: event %d = %q, want %q", sid, i, evs[i].Type, typ)
		}
	}
	return evs
}

func TestOpenWithTraceparentPropagatesToEveryEvent(t *testing.T) {
	h := newHarness(t, nil)
	cs := connectMCP(t, h.svc)
	sid, addr := mcpOpen(t, cs, mcp.Meta{"traceparent": tpOne})
	driveUtterance(t, h, sid, addr)
	evs := finish(t, h, cs, sid)
	for _, ev := range evs {
		if ev.Traceparent != tpOne {
			t.Fatalf("%s: ce_traceparent = %q, want exactly %q", ev.Type, ev.Traceparent, tpOne)
		}
	}
}

func TestOpenWithoutMetaHasNoTraceparent(t *testing.T) {
	h := newHarness(t, nil)
	cs := connectMCP(t, h.svc)
	sid, addr := mcpOpen(t, cs, nil)
	driveUtterance(t, h, sid, addr)
	evs := finish(t, h, cs, sid)
	for _, ev := range evs {
		if ev.Traceparent != "" {
			t.Fatalf("%s: ce_traceparent = %q, want the field absent (no made-up value)", ev.Type, ev.Traceparent)
		}
	}
}

func TestTwoSessionsKeepTheirOwnTraceparent(t *testing.T) {
	h := newHarness(t, nil)
	cs := connectMCP(t, h.svc)

	sidA, addrA := mcpOpen(t, cs, mcp.Meta{"traceparent": tpOne})
	sidB, addrB := mcpOpen(t, cs, mcp.Meta{"traceparent": tpOther})
	if sidA == sidB {
		t.Fatalf("two opens gave the same session id %q", sidA)
	}
	driveUtterance(t, h, sidA, addrA)
	driveUtterance(t, h, sidB, addrB)
	evsA := finish(t, h, cs, sidA)
	evsB := finish(t, h, cs, sidB)
	for _, ev := range evsA {
		if ev.Traceparent != tpOne {
			t.Fatalf("session A %s: ce_traceparent = %q, want %q", ev.Type, ev.Traceparent, tpOne)
		}
	}
	for _, ev := range evsB {
		if ev.Traceparent != tpOther {
			t.Fatalf("session B %s: ce_traceparent = %q, want %q", ev.Type, ev.Traceparent, tpOther)
		}
	}
}
