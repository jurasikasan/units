package senml

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

func TestBaseFieldsAndAbsoluteTime(t *testing.T) {
	body := `[{"bn":"urn:dev:cam1:","bt":1764237600.25,"n":"description","vs":"torchlight flickers"},
	          {"n":"read","t":1.5,"vs":"BEWARE"}]`
	recs, err := Parse([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0].Name != "urn:dev:cam1:description" || recs[0].Kind() != "description" {
		t.Errorf("name %q kind %q", recs[0].Name, recs[0].Kind())
	}
	want := time.Date(2025, 11, 27, 10, 0, 0, 250e6, time.UTC)
	if !recs[0].Time.Equal(want) {
		t.Errorf("time %s, want %s", recs[0].Time, want)
	}
	if !recs[1].Time.Equal(want.Add(1500 * time.Millisecond)) {
		t.Errorf("second record's time %s", recs[1].Time)
	}
	if !recs[1].HasString || recs[1].String != "BEWARE" || recs[1].Kind() != "read" {
		t.Errorf("record %+v", recs[1])
	}
}

// A time under 2**28 is relative to when the pack arrived (RFC 8428 §4.5.3).
func TestRelativeTime(t *testing.T) {
	recs, err := Parse([]byte(`[{"n":"description","t":-2.5,"vs":"x"},{"n":"read","vs":"y"}]`), now)
	if err != nil {
		t.Fatal(err)
	}
	if !recs[0].Time.Equal(now.Add(-2500 * time.Millisecond)) {
		t.Errorf("relative: %s", recs[0].Time)
	}
	if !recs[1].Time.Equal(now) {
		t.Errorf("no time is now: %s", recs[1].Time)
	}
}

func TestDataValue(t *testing.T) {
	payload := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}
	recs, err := Parse([]byte(`[{"n":"frame","vd":"`+EncodeData(payload)+`"}]`), now)
	if err != nil {
		t.Fatal(err)
	}
	if string(recs[0].Data) != string(payload) {
		t.Errorf("vd %x", recs[0].Data)
	}
}

func TestRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not json":           `{`,
		"not a pack":         `{"n":"x"}`,
		"no name":            `[{"vs":"x"}]`,
		"must-understand":    `[{"n":"x","vs":"y","cam_":1}]`,
		"bad vd":             `[{"n":"frame","vd":"***"}]`,
		"version from later": `[{"bver":99,"n":"x","vs":"y"}]`,
	} {
		if _, err := Parse([]byte(body), now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
