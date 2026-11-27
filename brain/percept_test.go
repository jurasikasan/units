package brain

import (
	"strings"
	"testing"
	"time"

	"github.com/jurasikasan/units/shared/events"
)

const digest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

var t0 = time.Date(2026, 11, 27, 21, 0, 12, 500_000_000, time.UTC)

// fields is an entry as a sense puts it on its stream: the real envelope.
func fields(t *testing.T, source, typ string, data map[string]any) map[string]any {
	t.Helper()
	f, err := events.Fields(source, events.Event{Type: typ, Subject: "s-1", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func ms(d int) time.Time { return t0.Add(time.Duration(d) * time.Millisecond) }

func stamp(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z") }

// A sense is known by its events' shape alone: any resource, any source.
func TestParseRecognitionOfAnySense(t *testing.T) {
	for _, tc := range []struct{ source, typ, channel string }{
		{"/senses/hearing", "speechrecog.recognition-complete", "hearing"},
		{"/senses/sight", "sightrecog.recognition-complete", "sight"},
		{"/senses/smell", "smellrecog.recognition-complete", "smell"},
		// A body's stream (body/): its senses' events republished from
		// /bodies/<name>/<channel> are senses like any other.
		{"/bodies/a/hearing", "speechrecog.recognition-complete", "hearing"},
		{"/bodies/a/sight", "sightrecog.recognition-complete", "sight"},
	} {
		ev, err := ParseEvent(fields(t, tc.source, tc.typ, map[string]any{
			"request_id": "r-1", "text": " hi ", "started_at": stamp(ms(0)), "ended_at": stamp(ms(600)),
		}), ms(900))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind != Recognition || ev.Channel != tc.channel || ev.Percept.Text != "hi" {
			t.Fatalf("%s: %+v", tc.source, ev)
		}
		p := ev.Percept
		if !p.Started.Equal(ms(0)) || !p.Ended.Equal(ms(600)) || p.Sent.IsZero() || !p.Received.Equal(ms(900)) {
			t.Fatalf("times: %+v", p)
		}
	}
}

func TestParseFrame(t *testing.T) {
	ev, err := ParseEvent(fields(t, "/senses/sight", "sightrecog.recognition-complete", map[string]any{
		"request_id": "obs-1", "text": "a corridor", "read": "EXIT", "input_type": "video",
		"started_at": stamp(ms(0)), "ended_at": stamp(ms(0)),
		"frame": map[string]any{"sha256": digest, "space": "sight", "name": "a.jpg", "content_type": "image/jpeg", "at": stamp(ms(0))},
	}), ms(10))
	if err != nil {
		t.Fatal(err)
	}
	if f := ev.Percept.Frame; f == nil || f.SHA256 != digest || f.ContentType != "image/jpeg" {
		t.Fatalf("frame: %+v", ev.Percept)
	}

	ev, err = ParseEvent(fields(t, "/senses/sight", "sightrecog.recognition-complete", map[string]any{
		"request_id": "obs-2", "text": "x", "started_at": stamp(ms(0)),
		"frame": map[string]any{"sha256": strings.ToUpper(digest)},
	}), ms(10))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Percept.Frame != nil || ev.Percept.FrameError == "" {
		t.Fatalf("a bad sha256 is no frame: %+v", ev.Percept)
	}
	if !ev.Percept.Ended.Equal(ms(0)) {
		t.Fatalf("no ended_at is a moment: %v", ev.Percept.Ended)
	}
}

func TestParseStartAndIgnored(t *testing.T) {
	ev, err := ParseEvent(fields(t, "/senses/hearing", "speechrecog.start-of-input", map[string]any{
		"request_id": "r-1", "started_at": stamp(ms(0)),
	}), ms(0))
	if err != nil || ev.Kind != Start || ev.RequestID != "r-1" {
		t.Fatalf("%+v %v", ev, err)
	}
	ev, err = ParseEvent(fields(t, "/senses/hearing", "speechrecog.session-closed", map[string]any{"reason": "idle"}), ms(0))
	if err != nil || ev.Kind != Ignored {
		t.Fatalf("%+v %v", ev, err)
	}
	// A body's own acts on its stream are not percepts: what came of an act
	// is perceived through the senses, and remembered by the brain itself.
	ev, err = ParseEvent(fields(t, "/bodies/a", "body.act-complete", map[string]any{"tool": "skip_turn"}), ms(0))
	if err != nil || ev.Kind != Ignored {
		t.Fatalf("%+v %v", ev, err)
	}
}

func TestParseMalformed(t *testing.T) {
	cases := map[string]map[string]any{
		"no source":      {"ce_type": "x.recognition-complete", "ce_id": "1", "data": "{}"},
		"no id":          {"ce_type": "x.recognition-complete", "ce_source": "/s", "data": "{}"},
		"data not json":  {"ce_type": "x.recognition-complete", "ce_source": "/s", "ce_id": "1", "data": "{"},
		"no started_at":  {"ce_type": "x.recognition-complete", "ce_source": "/s", "ce_id": "1", "data": `{"text":"hi"}`},
		"source is root": {"ce_type": "x.recognition-complete", "ce_source": "/", "ce_id": "1", "data": "{}"},
	}
	for name, f := range cases {
		if _, err := ParseEvent(f, ms(0)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLine(t *testing.T) {
	cases := []struct {
		p    Percept
		want string
	}{
		{Percept{Channel: "hearing", Started: ms(0), Text: "stop", InputType: "speech"}, `[21:00:12.500 hearing] "stop"`},
		{Percept{Channel: "sight", Started: ms(0), Text: "a person in the corridor", InputType: "video"}, `[21:00:12.500 sight] a person in the corridor`},
		{Percept{Channel: "sight", Started: ms(0), Text: "a door", Read: "EXIT"}, `[21:00:12.500 sight] a door; read: "EXIT"`},
		{Percept{Channel: "sight", Started: ms(0), Cause: "recognizer-error", Frame: &Frame{}}, `[21:00:12.500 sight] (no words: recognizer-error)`},
	}
	for _, tc := range cases {
		if got := Line(tc.p); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
}
