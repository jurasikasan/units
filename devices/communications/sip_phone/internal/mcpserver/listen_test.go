package mcpserver

// The listen tool as an MCP client sees it (contract revision 3, §2.3):
// errors are tool errors starting with unknown_call / not_answered /
// bad_target; on success the result is {"ok": true} and to:"" is the stop.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/farend"
)

func TestListenToolContract(t *testing.T) {
	cs, p := connect(t)

	_, errText := callTool(t, cs, "listen", map[string]any{"call_id": "call-ffffffff", "to": "127.0.0.1:42000"}, nil)
	assert.True(t, strings.HasPrefix(errText, "unknown_call:"), errText)

	out, _ := callTool(t, cs, "dial", map[string]any{"to": farend.RingForever}, nil)
	ringingID, _ := out["call_id"].(string)
	p.waitFor(t, events.CallRinging)
	_, errText = callTool(t, cs, "listen", map[string]any{"call_id": ringingID, "to": "127.0.0.1:42000"}, nil)
	assert.True(t, strings.HasPrefix(errText, "not_answered:"), errText)
	callTool(t, cs, "hangup", map[string]any{"call_id": ringingID}, nil)

	out, _ = callTool(t, cs, "dial", map[string]any{"to": farend.AnswerAndHold}, nil)
	id, _ := out["call_id"].(string)
	p.waitFor(t, events.CallAnswered)

	_, errText = callTool(t, cs, "listen", map[string]any{"call_id": id, "to": "x"}, nil)
	assert.True(t, strings.HasPrefix(errText, "bad_target:"), errText)
	_, errText = callTool(t, cs, "listen", map[string]any{"call_id": id, "to": "127.0.0.1"}, nil)
	assert.True(t, strings.HasPrefix(errText, "bad_target:"), errText)

	_, lnPort := farend.ListenUDP(t)
	out, errText = callTool(t, cs, "listen", map[string]any{"call_id": id, "to": fmt.Sprintf("127.0.0.1:%d", lnPort)}, nil)
	require.Empty(t, errText)
	assert.Equal(t, map[string]any{"ok": true}, out)

	out, errText = callTool(t, cs, "listen", map[string]any{"call_id": id, "to": ""}, nil)
	require.Empty(t, errText, "to:\"\" stops the stream and is not an error")
	assert.Equal(t, map[string]any{"ok": true}, out)

	callTool(t, cs, "hangup", map[string]any{"call_id": id}, nil)
	p.waitFor(t, events.CallEnded)
}
