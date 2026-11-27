package brain

import (
	"testing"
	"time"
)

func percept(ch string, start, end int) Percept {
	return Percept{Channel: ch, Started: ms(start), Ended: ms(end), Text: ch}
}

// A percept lives for its channel's lifetime on that channel's own clock:
// a newer percept of the same channel ages it, another channel's does not.
func TestBuffersExpireOnTheChannelsClock(t *testing.T) {
	c := &Counts{}
	b := &Buffers{Keep: map[string]time.Duration{"sight": time.Second}, KeepDefault: 4 * time.Second, Counts: c}
	b.Add(percept("sight", 0, 0))
	b.Add(percept("hearing", 0, 600))
	b.Add(percept("sight", 900, 900)) // 0.9 s later: the first still lives
	if b.Len() != 3 {
		t.Fatalf("len %d", b.Len())
	}
	b.Add(percept("sight", 1500, 1500)) // 1.5 s after the first: it is gone
	b.Add(percept("hearing", 3000, 4000))
	if b.Len() != 4 || c.Expired.Load() != 1 {
		t.Fatalf("len %d expired %d", b.Len(), c.Expired.Load())
	}
	b.Add(percept("hearing", 5000, 5000)) // 4.4 s after the first hearing ended
	if c.Expired.Load() != 2 {
		t.Fatalf("expired %d", c.Expired.Load())
	}
}

func TestBuffersAreBounded(t *testing.T) {
	c := &Counts{}
	b := &Buffers{KeepDefault: time.Hour, Max: 3, Counts: c}
	for i := 0; i < 5; i++ {
		b.Add(percept("hearing", i, i))
	}
	got := b.Take()
	if len(got) != 3 || c.Overflow.Load() != 2 || !got[0].Started.Equal(ms(2)) {
		t.Fatalf("%d kept, %d over, first %v", len(got), c.Overflow.Load(), got[0].Started)
	}
}

// Take gives everything in perception order, whatever order it came in.
func TestTakeOrdersByPerceptionTime(t *testing.T) {
	b := &Buffers{KeepDefault: time.Hour, Counts: &Counts{}}
	b.Add(percept("sight", 300, 300))
	b.Add(percept("hearing", 0, 600))
	b.Add(percept("sight", 100, 100))
	got := b.Take()
	if got[0].Channel != "hearing" || !got[1].Started.Equal(ms(100)) || !got[2].Started.Equal(ms(300)) {
		t.Fatalf("%+v", got)
	}
	if b.Len() != 0 {
		t.Fatal("not emptied")
	}
}

func TestBind(t *testing.T) {
	n := func(ms [][]Percept) []int {
		var out []int
		for _, m := range ms {
			out = append(out, len(m))
		}
		return out
	}
	bind := 200 * time.Millisecond
	cases := []struct {
		name string
		ps   []Percept
		want []int
	}{
		{"overlapping channels are one moment", []Percept{percept("hearing", 0, 600), percept("sight", 100, 100)}, []int{2}},
		{"within bind is one moment", []Percept{percept("hearing", 0, 600), percept("sight", 750, 750)}, []int{2}},
		{"past bind is another", []Percept{percept("hearing", 0, 600), percept("sight", 900, 900)}, []int{1, 1}},
		{"one channel alone is two moments", []Percept{percept("hearing", 0, 600), percept("hearing", 650, 900)}, []int{1, 1}},
		{"another channel between binds all three", []Percept{percept("hearing", 0, 600), percept("sight", 500, 500), percept("hearing", 650, 900)}, []int{3}},
	}
	for _, tc := range cases {
		got := n(Bind(tc.ps, bind))
		if len(got) != len(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestParseConfig(t *testing.T) {
	closed, err := ParseGate("sight:closed, hearing:open")
	if err != nil || !closed["sight"] || closed["hearing"] {
		t.Fatalf("%v %v", closed, err)
	}
	if _, err := ParseGate("sight:shut"); err == nil {
		t.Fatal("shut is no state")
	}
	keep, def, err := ParseKeep("hearing:4s,sight:1s,2s", time.Second)
	if err != nil || keep["sight"] != time.Second || keep["hearing"] != 4*time.Second || def != 2*time.Second {
		t.Fatalf("%v %v %v", keep, def, err)
	}
	if _, _, err := ParseKeep("sight:-1s", time.Second); err == nil {
		t.Fatal("a negative lifetime")
	}
	s, err := ParseSenders("hearing:00000000-0000-0000-0000-0000000000a1")
	if err != nil || s["hearing"].String() != "00000000-0000-0000-0000-0000000000a1" {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := ParseSenders("hearing:me"); err == nil {
		t.Fatal("not a uuid")
	}
	c := Config{Senders: s}
	if c.SubjectFor("hearing") != s["hearing"] || c.SubjectFor("smell") != DerivedSubject("channel:smell") {
		t.Fatal("subjects")
	}
}
