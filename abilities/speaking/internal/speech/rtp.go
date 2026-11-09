package speech

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// The wire format (C4 §4): L16 — 48 kHz mono PCM16, network byte order,
// payload type 96, 20 ms per packet. 20 ms at 48 kHz is 960 samples, i.e.
// 1920 bytes of payload per packet.
const (
	payloadType  = 96
	frameSamples = 960 // 20 ms at 48 kHz
	frameBytes   = frameSamples * 2
	sampleRate   = 48000
)

// rtpSink sends a session's audio to the phone: one RTP stream, one SSRC,
// one packet per 20 ms frame. It only sends — speech opens no inbound port
// for audio (C4 §5).
type rtpSink struct {
	conn   *net.UDPConn
	target *net.UDPAddr
	ssrc   uint32

	mu   sync.Mutex
	seq  uint16
	ts   uint32
	last time.Time
}

func newRTPSink(ssrc uint32, target *net.UDPAddr) *rtpSink {
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		// A send-only UDP bind failing means the machine has no UDP at all;
		// the sink reports it per write and the mouth ends the utterance.
		slog.Warn("rtp sink without a local socket", "err", err)
		return &rtpSink{target: target, ssrc: ssrc}
	}
	return &rtpSink{conn: conn, target: target, ssrc: ssrc}
}

// Close releases the session's UDP socket. The sink had exactly this one
// socket for its whole life (one per session), and every write after Close
// fails.
func (r *rtpSink) Close() {
	r.mu.Lock()
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
	}
	r.mu.Unlock()
}

// WriteFrame sends one 960-sample frame as one RTP packet. The RTP
// timestamp is the audio clock at 48 kHz: it advances by the wall time that
// passed since the last frame (at least one frame's worth), so silence
// between utterances shows up as a gap in the timestamp, not a jump (C4 §4).
func (r *rtpSink) WriteFrame(pcm []int16) error {
	if len(pcm) != frameSamples {
		return fmt.Errorf("speech: an rtp frame is %d samples, got %d", frameSamples, len(pcm))
	}
	now := time.Now()

	payload := make([]byte, frameBytes)
	for i, s := range pcm {
		binary.BigEndian.PutUint16(payload[i*2:], uint16(s))
	}

	r.mu.Lock()
	if !r.last.IsZero() {
		since := now.Sub(r.last)
		steps := int((since + time.Millisecond) / (20 * time.Millisecond))
		if steps < 1 {
			steps = 1
		}
		r.ts += uint32(steps) * sampleRate * 20 / 1000 // steps * 960
	}
	r.last = now
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType,
			SequenceNumber: r.seq,
			Timestamp:      r.ts,
			SSRC:           r.ssrc,
		},
		Payload: payload,
	}
	r.seq++
	conn := r.conn
	r.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("speech: no udp socket to send on")
	}
	buf, err := pkt.Marshal()
	if err != nil {
		return err
	}
	_, err = conn.WriteTo(buf, r.target)
	return err
}
