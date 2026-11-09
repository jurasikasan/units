package g711

import "testing"

// Anchor values from the task specification (C5 section 3):
// ulaw 0xFF -> 0, 0x80 -> 32124, 0x00 -> -32124; alaw 0xD5 -> 8, 0x55 -> -8.
func TestUlawAnchors(t *testing.T) {
	cases := []struct {
		in   byte
		want int16
	}{
		{0xFF, 0},
		{0x80, 32124},
		{0x00, -32124},
	}
	for _, c := range cases {
		if got := Ulaw(c.in); got != c.want {
			t.Errorf("Ulaw(0x%02x) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAlawAnchors(t *testing.T) {
	cases := []struct {
		in   byte
		want int16
	}{
		{0xD5, 8},
		{0x55, -8},
	}
	for _, c := range cases {
		if got := Alaw(c.in); got != c.want {
			t.Errorf("Alaw(0x%02x) = %d, want %d", c.in, got, c.want)
		}
	}
}
