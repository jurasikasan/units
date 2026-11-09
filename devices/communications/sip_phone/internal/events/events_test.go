package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The field names and values come from contracts/events.md.
func TestFields_Envelope(t *testing.T) {
	at := time.Date(2026, 10, 29, 10, 15, 3, 120_000_000, time.FixedZone("EET", 2*3600))
	f, err := Fields(Event{
		Type:        CallAnswered,
		Subject:     "call-0b8e41f2",
		Data:        map[string]any{"call_id": "call-0b8e41f2"},
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Time:        at,
	})
	require.NoError(t, err)

	assert.Equal(t, "1.0", f["ce_specversion"])
	assert.Equal(t, "/devices/communications/sip_phone", f["ce_source"])
	assert.Equal(t, "phone.call.answered", f["ce_type"])
	assert.Equal(t, "call-0b8e41f2", f["ce_subject"])
	assert.Equal(t, "2026-10-29T08:15:03.120Z", f["ce_time"])
	assert.Equal(t, "application/json", f["ce_datacontenttype"])
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", f["ce_traceparent"])
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, f["ce_id"])

	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(f["data"].(string)), &data))
	assert.Equal(t, "call-0b8e41f2", data["call_id"])
}

func TestFields_NoTraceparentNoField(t *testing.T) {
	f, err := Fields(Event{Type: CallEnded, Subject: "c", Data: map[string]any{}, Time: time.Now()})
	require.NoError(t, err)
	_, ok := f["ce_traceparent"]
	assert.False(t, ok)
}
