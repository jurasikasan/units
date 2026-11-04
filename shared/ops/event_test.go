package ops

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalise(t *testing.T) {
	tests := []struct {
		name     string
		in       Event
		wantErr  bool
		wantSev  Severity
		wantSvc  string
		reasonIs string
	}{
		{
			name:    "missing severity defaults to error",
			in:      Event{Service: "ocr_service", Stage: "ocr", Reason: "tesseract exited 1"},
			wantSev: SeverityError,
			wantSvc: "ocr_service",
		},
		{
			name:    "warn is preserved",
			in:      Event{Service: "email_intake", Stage: "poll", Severity: SeverityWarn, Reason: "duplicate mail"},
			wantSev: SeverityWarn,
			wantSvc: "email_intake",
		},
		{
			name:     "surrounding whitespace is trimmed",
			in:       Event{Service: "  api  ", Stage: " send ", Reason: "  boom  "},
			wantSev:  SeverityError,
			wantSvc:  "api",
			reasonIs: "boom",
		},
		{name: "no service is rejected", in: Event{Stage: "s", Reason: "r"}, wantErr: true},
		{name: "no stage is rejected", in: Event{Service: "s", Reason: "r"}, wantErr: true},
		{name: "no reason is rejected", in: Event{Service: "s", Stage: "t"}, wantErr: true},
		{
			name:    "unknown severity is rejected rather than coerced",
			in:      Event{Service: "s", Stage: "t", Severity: "critical", Reason: "r"},
			wantErr: true,
		},
		{
			name:    "reason over 200 chars looks like a payload and is rejected",
			in:      Event{Service: "s", Stage: "t", Reason: strings.Repeat("a", 201)},
			wantErr: true,
		},
		{
			name:    "reason at exactly the 200-char limit is still a sentence",
			in:      Event{Service: "s", Stage: "t", Reason: strings.Repeat("a", 200)},
			wantSev: SeverityError,
			wantSvc: "s",
			wantErr: false,
		},
		{
			name:    "reason with a newline is a payload, not a sentence",
			in:      Event{Service: "s", Stage: "t", Reason: "line one\nline two"},
			wantErr: true,
		},
		{
			name:    "reason with a carriage return is also a payload",
			in:      Event{Service: "s", Stage: "t", Reason: "line one\rline two"},
			wantErr: true,
		},
		{

			name:    "existing publisher reasons pass",
			in:      Event{Service: "s", Stage: "t", Reason: "model returned prose instead of JSON, fell back to substring match"},
			wantSev: SeverityError,
			wantSvc: "s",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.in
			err := e.Normalise()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantSev, e.Severity)
			assert.Equal(t, tc.wantSvc, e.Service)
			if tc.reasonIs != "" {
				assert.Equal(t, tc.reasonIs, e.Reason)
			}
		})
	}
}

func TestValuesRoundTrip(t *testing.T) {
	in := Event{
		Service: "classifier", Stage: "classify", Severity: SeverityError,
		Entity: "document", EntityID: "7f3a", Reason: "model returned prose",
	}
	out, err := FromValues(in.Values())
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestFromValuesIgnoresUnknownFields(t *testing.T) {

	v := map[string]any{
		"service": "s", "stage": "t", "severity": "error", "reason": "r",
		"something_new": "ignored", "trace_id": "abc",
	}
	e, err := FromValues(v)
	require.NoError(t, err)
	assert.Equal(t, "r", e.Reason)
}

func TestDedupeKeyExcludesEntityID(t *testing.T) {

	a := Event{Service: "s", Stage: "t", Reason: "r", EntityID: "id-1"}
	b := Event{Service: "s", Stage: "t", Reason: "r", EntityID: "id-2"}
	assert.Equal(t, a.DedupeKey(), b.DedupeKey())

	c := Event{Service: "s", Stage: "t", Reason: "different"}
	assert.NotEqual(t, a.DedupeKey(), c.DedupeKey())
}
