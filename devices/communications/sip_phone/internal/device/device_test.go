// audio_in tests for phone: the line carries a two-way audio pipe.
//
// Expected values come from the contract (§2.2), not from this code:
//   - the answered event carries audio_in = "<host>:<port>" with the port in
//     the configured range (here 127.0.0.1:430xx-431xx);
//   - 50 packets of a 440 Hz sine, amplitude 8000, as L16/48 kHz (960 samples,
//     big-endian, PT 96) sent to audio_in arrive at the far end as G.711 RTP
//     packets of 160 samples; decoded at 8 kHz the signal has 880±44 zero
//     crossings per second and RMS within 20% of 5657;
//   - packets with a different SSRC or a different payload type are not
//     forwarded;
//   - after hangup (ours) or BYE (theirs) the port is not forwarded and is
//     free for the next call;
//   - while audio_in is quiet the leg carries G.711 silence, 20 ms apart, on the
//     same stream (revision of 2026-11-02: holding the line is the phone's).
package device

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/call"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/farend"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/rtp"
)

const (
	freq = 440.0
	amp  = 8000
)

type recorder struct {
	ch chan events.Event
}

func (r *recorder) Publish(_ context.Context, e events.Event) error {
	r.ch <- e
	return nil
}

func (r *recorder) waitFor(t *testing.T, typ string) events.Event {
	t.Helper()
	for {
		select {
		case e := <-r.ch:
			if e.Type == typ {
				return e
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s", typ)
		}
	}
}

// A phone wired as in production, against the in-process far end, with audio_in
// on the loopback host inside the task's default port range.
func newDevice(t *testing.T, media *net.UDPConn) (*Device, *recorder) {
	t.Helper()
	fe := farend.Start(t)
	fe.AnswerMediaPort.Store(int32(media.LocalAddr().(*net.UDPAddr).Port))
	conn, port := farend.ListenUDP(t)
	ua, err := sipgo.NewUA()
	require.NoError(t, err)
	srv, err := sipgo.NewServer(ua)
	require.NoError(t, err)
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(port))
	require.NoError(t, err)

	rec := &recorder{ch: make(chan events.Event, 64)}
	m := call.NewManager(call.Config{
		Trunk:          fe.Addr,
		AdvertiseHost:  "127.0.0.1",
		SIPPort:        port,
		MaxCalls:       2,
		RingDefault:    3 * time.Second,
		StripPlus:      true,
		AudioInHost:    "127.0.0.1",
		AudioInPortMin: 43000,
		AudioInPortMax: 43199,
	}, cli, rec)
	srv.OnBye(m.OnBye)
	go srv.ServeUDP(conn) //nolint:errcheck
	t.Cleanup(func() { m.Wait(); _ = ua.Close(); _ = conn.Close() })
	return New(m), rec
}

// listenMedia binds a free loopback port for the far end to answer with (newDevice)
// and returns it, ready to read whatever phone sends down the call's RTP leg.
func listenMedia(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readMedia reads one RTP packet from the far end media socket, or nil if none
// arrives before the timeout.
func readMedia(t *testing.T, conn *net.UDPConn, timeout time.Duration) *pionrtp.Packet {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	var pkt pionrtp.Packet
	require.NoError(t, pkt.Unmarshal(buf[:n]))
	return &pkt
}

// readAudio reads the next packet that is not silence, or nil if none arrives
// before the timeout: while audio_in is quiet phone fills the leg with silence.
func readAudio(t *testing.T, conn *net.UDPConn, timeout time.Duration) *pionrtp.Packet {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		pkt := readMedia(t, conn, left)
		if pkt == nil || !isSilence(pkt) {
			return pkt
		}
	}
}

func isSilence(pkt *pionrtp.Packet) bool {
	for _, s := range rtp.PayloadType(pkt.PayloadType).DecodePCM(pkt.Payload) {
		if s > 16 || s < -16 {
			return false
		}
	}
	return true
}

// drain reads until the leg has been quiet for quiet.
func drain(t *testing.T, conn *net.UDPConn, quiet time.Duration) {
	t.Helper()
	for readMedia(t, conn, quiet) != nil {
	}
}

// sinePacket is one 20 ms audio_in frame: 960 samples of the 440 Hz sine at
// amplitude 8000, big-endian int16 (L16, RFC 3551).
func sinePacket(offset int) []byte {
	out := make([]byte, 960*2)
	for j := 0; j < 960; j++ {
		n := offset + j
		s := int16(amp * math.Sin(2*math.Pi*freq*float64(n)/48000))
		binary.BigEndian.PutUint16(out[j*2:], uint16(uint32(s)))
	}
	return out
}

// sendAudioIn sends one RTP packet (payload type pt, source ssrc) to audio_in.
func sendAudioIn(t *testing.T, dst string, ssrc uint32, pt uint8, payload []byte) {
	t.Helper()
	udp, err := net.ResolveUDPAddr("udp", dst)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp", nil, udp)
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	hdr := pionrtp.Header{Version: 2, PayloadType: pt, SSRC: ssrc}
	b, err := hdr.Marshal()
	require.NoError(t, err)
	_, err = conn.Write(append(b, payload...))
	require.NoError(t, err)
}

func audioInPort(t *testing.T, data map[string]any) string {
	t.Helper()
	require.Contains(t, data, "audio_in", "answered event must carry audio_in")
	ai, ok := data["audio_in"].(string)
	require.True(t, ok, "audio_in must be a string, got %T", data["audio_in"])
	require.Regexp(t, regexp.MustCompile(`^127\.0\.0\.1:43[01]\d\d$`), ai,
		"audio_in must be the loopback host with a port in the configured range")
	return ai
}

func TestAudioInCarriesAudioIntoTheCall(t *testing.T) {
	media := listenMedia(t)
	d, rec := newDevice(t, media)

	id, err := d.Dial(call.DialRequest{To: farend.AnswerAndHold})
	require.NoError(t, err)
	// AnswerAndHold keeps the call open until we hang up; make sure a late
	// failure still lets m.Wait() in cleanup finish.
	t.Cleanup(func() { d.Hangup(id) })
	audioIn := audioInPort(t, rec.waitFor(t, events.CallAnswered).Data)

	// 50 packets = 1 s of 440 Hz at 48 kHz, one sender.
	const n = 50
	ssrcA := uint32(0xA11CE)
	for i := 0; i < n; i++ {
		sendAudioIn(t, audioIn, ssrcA, 96, sinePacket(i*960))
	}

	// Far end must receive n G.711 frames of 160 samples each.
	var samples []int16
	ssrcs := map[uint32]bool{}
	for i := 0; i < n; i++ {
		pkt := readAudio(t, media, time.Second)
		require.NotNil(t, pkt, "packet %d must be forwarded to the far end", i)
		require.Len(t, pkt.Payload, 160, "G.711 8 kHz 20 ms = 160 samples")
		require.Equal(t, uint8(8), pkt.PayloadType, "far end is PCMA per its SDP answer")
		ssrcs[pkt.SSRC] = true
		samples = append(samples, rtp.PayloadTypePCMA.DecodePCM(pkt.Payload)...)
	}
	require.Len(t, samples, 160*n)
	require.Len(t, ssrcs, 1, "the far end leg must carry a single SSRC")

	// The forwarded signal is the 440 Hz sine at 8 kHz: 880 zero crossings per
	// second (tolerance ±44) and RMS within 20% of 5657.
	crossings := 0
	for i := 1; i < len(samples); i++ {
		if (samples[i-1] < 0 && samples[i] >= 0) || (samples[i-1] >= 0 && samples[i] < 0) {
			crossings++
		}
	}
	cps := float64(crossings) / float64(len(samples)) * 8000
	require.InDelta(t, 880.0, cps, 44.0, "forwarded signal must be 440 Hz at 8 kHz, got %.1f zero crossings/s", cps)
	sum := float64(0)
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	require.InDelta(t, 5657.0, rms, 0.2*5657.0, "amplitude must survive resample+encode, RMS=%.0f", rms)

	// A second sender is not admitted: its packets never reach the far end.
	ssrcB := uint32(0xBEEF)
	sendAudioIn(t, audioIn, ssrcB, 96, sinePacket(9600))
	require.Nil(t, readAudio(t, media, 300*time.Millisecond), "a second SSRC must not be forwarded")

	// A different payload type is dropped even from the owning SSRC.
	sendAudioIn(t, audioIn, ssrcA, 0, sinePacket(10560))
	require.Nil(t, readAudio(t, media, 300*time.Millisecond), "payload type != 96 must not be forwarded")

	// Hangup (ours) ends the call and closes audio_in.
	d.Hangup(id)
	rec.waitFor(t, events.CallEnded)
	drain(t, media, 100*time.Millisecond) // silence sent before the end

	// Nothing is forwarded after the call is over...
	sendAudioIn(t, audioIn, ssrcA, 96, sinePacket(11520))
	require.Nil(t, readMedia(t, media, 300*time.Millisecond), "audio_in must be closed after hangup")
	// ...and the port is free for the next call.
	requirePortFree(t, audioIn[strings.LastIndexByte(audioIn, ':')+1:])
}

func TestFarEndBYEClosesAudioIn(t *testing.T) {
	media := listenMedia(t)
	d, rec := newDevice(t, media)

	_, err := d.Dial(call.DialRequest{To: farend.AnswerThenBye})
	require.NoError(t, err)
	audioIn := audioInPort(t, rec.waitFor(t, events.CallAnswered).Data)

	// The far end hangs up ~150 ms after answering.
	rec.waitFor(t, events.CallEnded)
	drain(t, media, 100*time.Millisecond) // silence sent before the end

	// audio_in is closed: no forwarding, and the port can be re-bound.
	sendAudioIn(t, audioIn, 0xC0FFEE, 96, sinePacket(12480))
	require.Nil(t, readMedia(t, media, 300*time.Millisecond), "audio_in must be closed after the far end's BYE")
	requirePortFree(t, audioIn[strings.LastIndexByte(audioIn, ':')+1:])
}

func requirePortFree(t *testing.T, port string) {
	t.Helper()
	udp, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+port)
	require.NoError(t, err)
	conn, err := net.ListenUDP("udp", udp)
	require.NoError(t, err, "audio_in port must be free after the call ended")
	_ = conn.Close()
}

// While audio_in is quiet phone keeps the call's RTP going with G.711 silence,
// one packet per 20 ms, on the leg's one stream; audio that arrives is forwarded
// and the fill resumes after it, with no break in the sequence.
func TestQuietAudioInKeepsTheLineAlive(t *testing.T) {
	media := listenMedia(t)
	d, rec := newDevice(t, media)
	id, err := d.Dial(call.DialRequest{To: farend.AnswerAndHold})
	require.NoError(t, err)
	t.Cleanup(func() { d.Hangup(id) })
	audioIn := audioInPort(t, rec.waitFor(t, events.CallAnswered).Data)

	var got []*pionrtp.Packet
	start := time.Now()
	for len(got) < 15 {
		pkt := readMedia(t, media, 200*time.Millisecond)
		require.NotNil(t, pkt, "nothing at audio_in: the leg must still carry RTP (packet %d)", len(got))
		require.True(t, isSilence(pkt), "packet %d is not silence", len(got))
		got = append(got, pkt)
	}
	require.InDelta(t, 300, float64(time.Since(start).Milliseconds()), 120, "15 packets of fill take about 300 ms")

	for i := 0; i < 5; i++ {
		sendAudioIn(t, audioIn, 0xA11CE, 96, sinePacket(i*960))
	}
	for i := 0; i < 5; i++ {
		pkt := readMedia(t, media, time.Second)
		for pkt != nil && isSilence(pkt) {
			got = append(got, pkt)
			pkt = readMedia(t, media, time.Second)
		}
		require.NotNil(t, pkt, "audio frame %d must be forwarded", i)
		got = append(got, pkt)
	}
	for i := 0; i < 5; i++ {
		pkt := readMedia(t, media, 200*time.Millisecond)
		require.NotNil(t, pkt, "the fill resumes after the audio")
		require.True(t, isSilence(pkt))
		got = append(got, pkt)
	}

	for i := 1; i < len(got); i++ {
		require.Equal(t, got[0].SSRC, got[i].SSRC, "one stream")
		require.Equal(t, got[i-1].SequenceNumber+1, got[i].SequenceNumber, "no break in the sequence at packet %d", i)
	}
}
