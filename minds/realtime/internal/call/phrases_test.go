package call

import (
	"context"
	"reflect"
	"testing"
)

func TestPhrasesBySentence(t *testing.T) {
	var p phrases
	var got []string
	for _, d := range []string{"Hello! I ", "am a fake", ". Pi is 3", ".14, roughly", "? Yes"} {
		got = append(got, p.feed(d)...)
	}
	got = append(got, p.flush()...)
	want := []string{"Hello!", "I am a fake.", "Pi is 3.14, roughly?", "Yes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHeardWholeAndPart(t *testing.T) {
	pieces := []*piece{
		{text: "The capital of France is Paris.", cause: "normal", startMs: 0, endMs: 2000},
		{text: "Anything else you would like?", spoken: "Anything else", cause: "barge-in", startMs: 2000, endMs: 2600},
	}
	cases := []struct {
		ms   int
		want string
	}{
		{0, ""},
		{1000, "The capital of"},
		{2000, "The capital of France is Paris."},
		{2300, "The capital of France is Paris. Anything"},
		{9000, "The capital of France is Paris. Anything else"},
	}
	for _, c := range cases {
		if got := heard(pieces, c.ms); got != c.want {
			t.Errorf("at %d ms: got %q, want %q", c.ms, got, c.want)
		}
	}
}

func TestShareCountsAWordMoreThanHalf(t *testing.T) {
	if got := share("один два три", 0.6); got != "один два" {
		t.Fatalf("got %q", got)
	}
	if got := share("один два три", 0.1); got != "" {
		t.Fatalf("got %q", got)
	}
}

// speak-complete arrived late, after 300 ms more sound had gone to the
// client: by it, the first phrase seems to end at 2300 and the second to
// start there. The marker placed the second at 2000, so the first ends there
// and 2100 ms is a little into the second, not the first's last word.
func TestHeardByMarkers(t *testing.T) {
	pieces := []*piece{
		{text: "The capital of France is Paris.", cause: "normal", startMs: 0, endMs: 2300, marked: true, placed: true},
		{text: "Anything else you would like?", cause: "normal", startMs: 2000, endMs: 3600, marked: true, placed: true},
	}
	cases := []struct {
		ms   int
		want string
	}{
		{1000, "The capital of"},
		{2000, "The capital of France is Paris."},
		{2100, "The capital of France is Paris."},
		{2400, "The capital of France is Paris. Anything"},
	}
	for _, c := range cases {
		if got := heard(pieces, c.ms); got != c.want {
			t.Errorf("at %d ms: got %q, want %q", c.ms, got, c.want)
		}
	}
	// Unplaced, the same times fall back on speak-complete's: 2000 ms is
	// inside the first phrase then, its last word not yet half heard.
	pieces[1].placed = false
	if got := heard(pieces, 2000); got != "The capital of France is" {
		t.Errorf("unplaced at 2000 ms: got %q", got)
	}
}

// A marker that comes before its packet places the phrase when the packet
// comes; one after, at once.
func TestMarkerPlacedEitherOrder(t *testing.T) {
	c := &Call{resp: &response{pieces: []*piece{{request: "a"}, {request: "b"}}}}
	r := c.resp
	c.marked("a", 1000) // before its sound
	c.sound(context.Background(), make([]byte, 20*bytesPerMs), 1000)
	c.sound(context.Background(), make([]byte, 20*bytesPerMs), 1960)
	c.marked("b", 1960) // after it
	if !r.pieces[0].placed || r.pieces[0].startMs != 0 {
		t.Errorf("a: %+v", r.pieces[0])
	}
	if !r.pieces[1].placed || r.pieces[1].startMs != 20 {
		t.Errorf("b: %+v", r.pieces[1])
	}
}
