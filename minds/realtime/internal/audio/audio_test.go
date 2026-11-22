package audio

import (
	"encoding/binary"
	"testing"
	"time"
)

func le(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func TestUpDoubles(t *testing.T) {
	got, last := Up(le(100, 200), 0)
	want := []int16{50, 100, 150, 200}
	if len(got) != len(want) || last != 200 {
		t.Fatalf("got %v last %d", got, last)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDownHalves(t *testing.T) {
	l16 := make([]byte, 8)
	for i, s := range []int16{100, 300, -100, -300} {
		binary.BigEndian.PutUint16(l16[2*i:], uint16(s))
	}
	got := Down(l16)
	if len(got) != 4 || int16(binary.LittleEndian.Uint16(got)) != 200 || int16(binary.LittleEndian.Uint16(got[2:])) != -200 {
		t.Fatalf("got %v", got)
	}
}

// What a Sender sends, a Receiver takes back: 100 ms at 24 kHz is five
// packets, and the sound comes back halved again.
func TestSenderToReceiver(t *testing.T) {
	ports := NewPorts(47900, 47910)
	r, err := ports.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := make(chan []byte, 16)
	go r.Run(func(pcm []byte, _ uint32) { got <- pcm })

	s, err := NewSender(r.Address("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	in := make([]int16, Rate/10)
	for i := range in {
		in[i] = 1000
	}
	if err := s.SendPCM24(le(in...)); err != nil {
		t.Fatal(err)
	}
	total := 0
	timeout := time.After(2 * time.Second)
	for total < 2*Rate/10 {
		select {
		case pcm := <-got:
			total += len(pcm)
		case <-timeout:
			t.Fatalf("%d bytes back, want %d", total, 2*Rate/10)
		}
	}
}

func TestPayloadRefusesOthers(t *testing.T) {
	if _, err := Payload([]byte{0x80, 0, 0, 0}); err == nil {
		t.Fatal("a short packet taken")
	}
	pkt := make([]byte, 14)
	pkt[0], pkt[1] = 0x80, 0 // PCMU
	if _, err := Payload(pkt); err == nil {
		t.Fatal("PCMU taken as L16")
	}
}
