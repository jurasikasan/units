package call

import (
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
