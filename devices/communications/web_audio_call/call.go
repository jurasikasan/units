package main

import (
	"encoding/binary"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"gopkg.in/hraban/opus.v2"
)

// wire rules for the line's own organ-facing RTP (listen output and audio_in):
// L16 48 kHz mono, payload type 96, 20 ms packets (960 samples = 1920 bytes).
const (
	l16PT           = 96
	samplesPerFrame = 960
)

// frameDuration converts a 48 kHz sample count to a duration.
func frameDuration(n int) time.Duration {
	return time.Duration(float64(n) / 48000 * float64(time.Second))
}

// Call is one live browser line: the WebRTC peer connection, the per-call
// audio_in UDP socket, and the optional RTP stream toward a listen target.
type Call struct {
	line      *Line
	id        string
	principal string
	pc        *webrtc.PeerConnection
	outTrack  *webrtc.TrackLocalStaticSample
	inConn    *net.UDPConn
	audioIn   string // "<AUDIO_IN_HOST>:<port>", what connected events report
	port      int
	ssrc      uint32 // one SSRC per call, used on the listen output
	startedAt time.Time

	active        atomic.Bool // true once the call is live; gates finish()
	connectedOnce atomic.Bool
	connectedAt   time.Time
	ended         atomic.Bool // set once, inside finish()

	enc *opus.Encoder // audio_in L16 -> Opus frames for the browser
	dec *opus.Decoder // browser Opus frames -> L16 for listen output

	log *slog.Logger

	mu         sync.Mutex
	track      *webrtc.TrackRemote
	trackPT    uint8
	haveInSSRC bool // first SSRC with PT 96 owns audio_in
	inSSRC     uint32
	unattended *time.Timer
	outConn    *net.UDPConn // current listen destination, nil = not listening
	outBuf     []byte       // decoded L16 bytes awaiting a full 20 ms frame
	outSeq     uint16
	outTS      uint32
}

// audioInLoop drains the call's audio_in UDP socket: it keeps the first SSRC
// with payload type 96, drops everything else, encodes each frame to Opus and
// writes it to the browser track. It exits when the socket closes with the call.
func (c *Call) audioInLoop() {
	buf := make([]byte, 65536)
	for {
		n, _, err := c.inConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		if pkt.PayloadType != l16PT {
			continue
		}
		c.mu.Lock()
		if c.ended.Load() {
			c.mu.Unlock()
			return
		}
		if !c.haveInSSRC {
			c.haveInSSRC = true
			c.inSSRC = pkt.SSRC
		} else if pkt.SSRC != c.inSSRC {
			c.mu.Unlock()
			continue
		}
		c.mu.Unlock()

		if len(pkt.Payload) == 0 || len(pkt.Payload)%2 != 0 {
			continue
		}
		nSamples := len(pkt.Payload) / 2
		samples := make([]int16, nSamples)
		for i := range samples {
			samples[i] = int16(binary.BigEndian.Uint16(pkt.Payload[2*i:]))
		}
		frame := make([]byte, 2*1024)
		nBytes, err := c.enc.Encode(samples, frame)
		if err != nil {
			c.log.Warn("audio_in encode failed", "err", err)
			continue
		}
		if nBytes > 0 {
			if err := c.outTrack.WriteSample(media.Sample{Data: frame[:nBytes], Duration: frameDuration(nSamples)}); err != nil {
				c.log.Warn("audio_in write to browser failed", "err", err)
			}
		}
		c.noteActivity()
	}
}

// onTrack keeps the first inbound track and starts consuming it.
func (c *Call) onTrack(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	c.mu.Lock()
	if c.track != nil {
		c.mu.Unlock()
		return
	}
	c.track = tr
	c.trackPT = uint8(tr.Codec().PayloadType)
	c.mu.Unlock()
	go c.consume(tr)
}

// consume decodes the browser's Opus to L16 and pushes it to the listen stream.
func (c *Call) consume(tr *webrtc.TrackRemote) {
	pcmBuf := make([]int16, 1152)
	for {
		pkt, _, err := tr.ReadRTP()
		if err != nil {
			return
		}
		if pkt.PayloadType != c.trackPT {
			continue
		}
		n, err := c.dec.Decode(pkt.Payload, pcmBuf)
		if err != nil {
			c.log.Warn("browser audio decode failed", "err", err)
			continue
		}
		if n == 0 {
			continue
		}
		c.noteActivity()
		c.pushSamples(pcmBuf[:n])
	}
}

// pushSamples appends decoded L16 samples and emits complete 20 ms frames as
// L16/PT 96 RTP packets to the current listen destination. Samples arrive in
// bursts (the browser decodes 20 ms at a time), so a small buffer makes the
// output cadence exact regardless of input timing. When there is no destination
// the audio is dropped.
func (c *Call) pushSamples(samples []int16) {
	var raw [][]byte
	var conn *net.UDPConn
	c.mu.Lock()
	if c.outConn == nil {
		c.outBuf = nil
		c.mu.Unlock()
		return
	}
	buf := make([]byte, len(c.outBuf), len(c.outBuf)+2*len(samples))
	copy(buf, c.outBuf)
	for _, s := range samples {
		v := uint16(s)
		buf = append(buf, byte(v>>8), byte(v))
	}
	for len(buf) >= 2*samplesPerFrame {
		frame := buf[:2*samplesPerFrame]
		pkt := rtp.Packet{
			Version:        2,
			PayloadType:    l16PT,
			SequenceNumber: c.outSeq,
			Timestamp:      c.outTS,
			SSRC:           c.ssrc,
			Payload:        frame,
		}
		c.outSeq++
		c.outTS += samplesPerFrame
		m, err := pkt.Marshal()
		if err != nil {
			c.log.Warn("listen RTP marshal failed", "err", err)
			buf = buf[2*samplesPerFrame:]
			continue
		}
		raw = append(raw, m)
		buf = buf[2*samplesPerFrame:]
	}
	c.outBuf = buf
	conn = c.outConn
	c.mu.Unlock()

	for _, p := range raw {
		if _, err := conn.Write(p); err != nil {
			_ = conn.Close()
			c.mu.Lock()
			if c.outConn == conn {
				c.outConn = nil
				c.outBuf = nil
			}
			c.mu.Unlock()
			c.log.Warn("listen destination lost, stream stopped", "err", err)
			return
		}
	}
}

// setDestination moves (conn nil stops) the listen stream for this call.
func (c *Call) setDestination(conn *net.UDPConn) {
	c.mu.Lock()
	old := c.outConn
	c.outConn = conn
	if conn == nil {
		c.outBuf = nil
	}
	c.mu.Unlock()
	if old != nil && old != conn {
		_ = old.Close()
	}
}

// noteActivity re-arms the unattended timer. It only counts once the call is
// connected; a finished call ignores it.
func (c *Call) noteActivity() {
	if !c.connectedOnce.Load() || c.ended.Load() {
		return
	}
	c.mu.Lock()
	if c.ended.Load() {
		c.mu.Unlock()
		return
	}
	if c.unattended != nil {
		c.unattended.Stop()
	}
	c.unattended = time.AfterFunc(c.line.cfg.UnattendedTimeout, func() {
		c.finish("unattended")
	})
	c.mu.Unlock()
}

// onState maps PeerConnection state to contract events.
func (c *Call) onState(s webrtc.PeerConnectionState) {
	switch s {
	case webrtc.PeerConnectionStateConnected:
		if c.connectedOnce.CompareAndSwap(false, true) {
			c.connectedAt = time.Now()
			c.line.emit(eventConnected, c, map[string]any{
				"call_id":      c.id,
				"principal":    c.principal,
				"audio_in":     c.audioIn,
				"connected_at": c.connectedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			})
			c.noteActivity()
		}
	case webrtc.PeerConnectionStateFailed:
		c.finish("failed")
	case webrtc.PeerConnectionStateClosed:
		if c.connectedOnce.Load() {
			c.finish("remote-hangup")
		} else {
			c.finish("failed")
		}
	case webrtc.PeerConnectionStateDisconnected:
		// Give the transport a grace period before calling it lost.
		time.AfterFunc(2*time.Second, func() {
			if c.ended.Load() {
				return
			}
			switch c.pc.ConnectionState() {
			case webrtc.PeerConnectionStateClosed:
				c.finish("remote-hangup")
			case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateDisconnected:
				c.finish("failed")
			}
		})
	}
}

// finish ends the call with reason, exactly once, and emits the ended event —
// the call's last event.
func (c *Call) finish(reason string) {
	if !c.ended.CompareAndSwap(false, true) {
		return
	}
	if !c.active.Load() {
		return
	}
	c.mu.Lock()
	if c.unattended != nil {
		c.unattended.Stop()
		c.unattended = nil
	}
	if c.outConn != nil {
		_ = c.outConn.Close()
		c.outConn = nil
	}
	c.outBuf = nil
	c.mu.Unlock()
	_ = c.inConn.Close()
	_ = c.pc.Close()
	c.line.releaseCall(c, reason)
}
