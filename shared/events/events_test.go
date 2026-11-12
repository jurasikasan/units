package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const uuidV4 = `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`

// The field names and values come from contracts/events.md, its whole entry.
func TestFields_Envelope(t *testing.T) {
	at := time.Date(2026, 10, 29, 10, 15, 3, 120_000_000, time.FixedZone("EET", 2*3600))
	f, err := Fields("/senses/hearing", Event{
		Type:        "speechrecog.recognition-complete",
		Subject:     "hs-0b8e41f2",
		Data:        map[string]any{"request_id": "utt-5c21d0e7", "text": "Hello?"},
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Time:        at,
	})
	require.NoError(t, err)

	assert.Equal(t, "1.0", f["ce_specversion"])
	assert.Equal(t, "/senses/hearing", f["ce_source"])
	assert.Equal(t, "speechrecog.recognition-complete", f["ce_type"])
	assert.Equal(t, "hs-0b8e41f2", f["ce_subject"])
	assert.Equal(t, "2026-10-29T08:15:03.120Z", f["ce_time"])
	assert.Equal(t, "application/json", f["ce_datacontenttype"])
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", f["ce_traceparent"])
	assert.Regexp(t, uuidV4, f["ce_id"])
	assert.Len(t, f, 9)

	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(f["data"].(string)), &data))
	assert.Equal(t, "Hello?", data["text"])
}

func TestFields_NoTraceparentNoField(t *testing.T) {
	f, err := Fields("/x", Event{Type: "x.closed", Subject: "s", Data: map[string]any{}})
	require.NoError(t, err)
	_, ok := f["ce_traceparent"]
	assert.False(t, ok)
}

func TestFields_ZeroTimeIsNow(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Millisecond)
	f, err := Fields("/x", Event{Type: "x.y", Subject: "s"})
	require.NoError(t, err)
	at, err := time.Parse("2006-01-02T15:04:05.000Z", f["ce_time"].(string))
	require.NoError(t, err)
	assert.False(t, at.Before(before))
	assert.WithinDuration(t, time.Now(), at, time.Second)
}

func TestFields_UnencodableData(t *testing.T) {
	_, err := Fields("/x", Event{Type: "x.y", Data: map[string]any{"f": func() {}}})
	assert.Error(t, err)
}

func TestFields_IDIsFreshPerEntry(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		f, err := Fields("/x", Event{Type: "x.y"})
		require.NoError(t, err)
		id := f["ce_id"].(string)
		require.Regexp(t, uuidV4, id)
		require.False(t, seen[id])
		seen[id] = true
	}
}
