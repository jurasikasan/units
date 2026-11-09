package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	shhttp "github.com/jurasikasan/units/shared/http"
	sredis "github.com/jurasikasan/units/shared/redis"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pion/webrtc/v4"
	"gopkg.in/hraban/opus.v2"
)

const (
	eventsStream   = "webrtc_line_events"
	eventConnected = "webrtc_line.call.connected"
	eventEnded     = "webrtc_line.call.ended"
	ceSource       = "/devices/communications/web_audio_call"
)

// Config carries the C6 §6 settings.
type Config struct {
	MaxCalls          int
	UnattendedTimeout time.Duration
	PublicIP          string // WEBRTC_PUBLIC_IP: advertised in candidates when set
	PortMin           int    // WEBRTC_PORT_MIN, media
	PortMax           int    // WEBRTC_PORT_MAX, media
	AudioInHost       string // WEBRTC_AUDIO_IN_HOST
	AudioInPortMin    int    // WEBRTC_AUDIO_IN_PORT_MIN
	AudioInPortMax    int    // WEBRTC_AUDIO_IN_PORT_MAX
}

func (c *Config) withDefaults() *Config {
	if c.MaxCalls <= 0 {
		c.MaxCalls = 4
	}
	if c.UnattendedTimeout <= 0 {
		c.UnattendedTimeout = 10 * time.Second
	}
	if c.PortMin <= 0 {
		c.PortMin = 45000
	}
	if c.PortMax < c.PortMin {
		c.PortMax = 45099
	}
	if c.AudioInHost == "" {
		c.AudioInHost = "webrtc_line"
	}
	if c.AudioInPortMin <= 0 {
		c.AudioInPortMin = 45100
	}
	if c.AudioInPortMax < c.AudioInPortMin {
		c.AudioInPortMax = 45199
	}
	return c
}

// Line is the service: the call registry, the audio_in port pool, the
// /offer door, /health and the MCP tools, all on one PORT.
type Line struct {
	cfg Config
	rdb *redis.Client
	log *slog.Logger
	api *webrtc.API

	mu      sync.Mutex
	calls   map[string]*Call
	endedAt map[string]time.Time // recently ended call ids -> ended time
	ports   map[int]struct{}     // audio_in ports in use

	mcpH http.Handler
}

// NewLine wires the service. rdb is injected so tests can point it at
// miniredis; production uses shared/redis.GetRedisClient.
func NewLine(cfg Config, rdb *redis.Client, log *slog.Logger) *Line {
	cfg = *cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}

	se := webrtc.SettingEngine{}
	if err := se.SetEphemeralUDPPortRange(uint16(cfg.PortMin), uint16(cfg.PortMax)); err != nil {
		log.Error("invalid media port range", "min", cfg.PortMin, "max", cfg.PortMax, "err", err)
	}

	impl := &mcp.Implementation{Name: "webrtc_line", Version: "0.1.0"}
	srv := mcp.NewServer(impl, nil)
	l := &Line{
		cfg:     cfg,
		rdb:     rdb,
		log:     log,
		api:     webrtc.NewAPI(webrtc.WithSettingEngine(se)),
		calls:   map[string]*Call{},
		endedAt: map[string]time.Time{},
		ports:   map[int]struct{}{},
	}
	mcp.AddTool[listenIn, okOut](srv, &mcp.Tool{
		Name: "listen",
		Description: "Start sending the call's browser microphone audio as RTP " +
			"(L16 48 kHz mono, payload type 96, 20 ms packets) to a host:port. " +
			"Another listen moves the stream to the new target; to \"\" stops it.",
	}, l.listenTool)
	mcp.AddTool[hangupIn, okOut](srv, &mcp.Tool{
		Name: "hangup",
		Description: "Hang up the call. Hanging up an already-ended call returns ok " +
			"without a second ended event.",
	}, l.hangupTool)
	l.mcpH = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return l
}

// handler is the single mux served on PORT.
func (l *Line) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /offer", l.handleOffer)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		shhttp.RespondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/mcp", l.mcpH)
	return mux
}

type offerRequest struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

func (l *Line) handleOffer(w http.ResponseWriter, r *http.Request) {
	principal := r.Header.Get("X-Principal-Id")
	if principal == "" {
		shhttp.RespondWithJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req offerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		shhttp.RespondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if req.Type != "offer" || req.SDP == "" {
		shhttp.RespondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a WebRTC offer"})
		return
	}
	l.mu.Lock()
	full := len(l.calls) >= l.cfg.MaxCalls
	l.mu.Unlock()
	if full {
		shhttp.RespondWithJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "capacity"})
		return
	}
	if !strings.Contains(req.SDP, "\nm=audio") {
		shhttp.RespondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "no audio"})
		return
	}
	call, answer, err := l.newCall(r.Context(), principal, req.SDP)
	if err != nil {
		l.log.Error("offer failed", "err", err)
		shhttp.RespondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "webrtc setup failed"})
		return
	}
	shhttp.RespondWithJSON(w, http.StatusOK, map[string]string{
		"sdp":     answer,
		"type":    "answer",
		"call_id": call.id,
	})
}

func newCallID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "wcall-" + hex.EncodeToString(b)
}

func randomSSRC() uint32 {
	var v uint32
	for v == 0 {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		v = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}
	return v
}

// newCall opens everything for one call and returns only after the non-trickle
// answer is fully gathered. Any failure leaves nothing behind.
func (l *Line) newCall(ctx context.Context, principal, offerSDP string) (*Call, string, error) {
	pc, err := l.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	var (
		inConn *net.UDPConn
		port   int
	)
	abort := func() {
		_ = pc.Close()
		if inConn != nil {
			_ = inConn.Close()
		}
		if port != 0 {
			l.mu.Lock()
			delete(l.ports, port)
			l.mu.Unlock()
		}
	}

	if inConn, port, err = l.bindAudioIn(); err != nil {
		_ = pc.Close()
		return nil, "", err
	}

	enc, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
	if err != nil {
		abort()
		return nil, "", err
	}
	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		abort()
		return nil, "", err
	}
	outTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "line")
	if err != nil {
		abort()
		return nil, "", err
	}

	call := &Call{
		line:      l,
		id:        newCallID(),
		principal: principal,
		pc:        pc,
		outTrack:  outTrack,
		inConn:    inConn,
		port:      port,
		audioIn:   l.cfg.AudioInHost + ":" + strconv.Itoa(port),
		ssrc:      randomSSRC(),
		startedAt: time.Now(),
		enc:       enc,
		dec:       dec,
		log:       l.log,
	}

	gathered := make(chan struct{})
	var gatheredOnce sync.Once
	pc.OnICEGatheringStateChange(func(s webrtc.ICEGatheringState) {
		if s == webrtc.ICEGatheringStateComplete {
			gatheredOnce.Do(func() { close(gathered) })
		}
	})
	pc.OnConnectionStateChange(call.onState)
	pc.OnTrack(call.onTrack)
	if _, err := pc.AddTrack(outTrack); err != nil {
		abort()
		return nil, "", err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		abort()
		return nil, "", err
	}
	answerSDP, err := pc.CreateAnswer(nil)
	if err != nil {
		abort()
		return nil, "", err
	}
	if err := pc.SetLocalDescription(answerSDP); err != nil {
		abort()
		return nil, "", err
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		abort()
		return nil, "", fmt.Errorf("ICE gathering timed out")
	case <-ctx.Done():
		abort()
		return nil, "", ctx.Err()
	}

	// Non-trickle answer: candidates are all in the SDP. When WEBRTC_PUBLIC_IP
	// is set (behind NAT), advertise it as the host candidate address; ICE
	// binds by ufrag/pwd + local port, not the advertised IP.
	answer := pc.LocalDescription().SDP
	answer = rewriteCandidateIPs(answer, l.cfg.PublicIP)

	l.mu.Lock()
	l.calls[call.id] = call
	l.mu.Unlock()
	call.active.Store(true)
	go call.audioInLoop()
	return call, answer, nil
}

// rewriteCandidateIPs replaces the address in host UDP candidate lines.
// SDP lines end in CRLF (RFC 4566 §5): the line is cut and rejoined on CRLF so
// the "host" token is not "host\r" and no empty line is added at the end — a
// browser refuses the whole answer for either.
func rewriteCandidateIPs(sdp, ip string) string {
	if ip == "" {
		return sdp
	}
	lines := strings.Split(sdp, "\r\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		f := strings.Split(line, " ")
		if len(f) >= 9 && strings.EqualFold(f[2], "udp") && f[6] == "typ" && f[7] == "host" {
			f[4] = ip
			lines[i] = strings.Join(f, " ")
		}
	}
	return strings.Join(lines, "\r\n")
}

// bindAudioIn allocates and binds the next free audio_in UDP port.
func (l *Line) bindAudioIn() (*net.UDPConn, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p := l.cfg.AudioInPortMin; p <= l.cfg.AudioInPortMax; p++ {
		if _, busy := l.ports[p]; busy {
			continue
		}
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: p})
		if err != nil {
			continue
		}
		l.ports[p] = struct{}{}
		return conn, p, nil
	}
	return nil, 0, fmt.Errorf("no free audio_in port in %d..%d", l.cfg.AudioInPortMin, l.cfg.AudioInPortMax)
}

// releaseCall unregisters the call, frees its audio_in port, remembers the id
// (so a late hangup still returns ok) and emits the ended event.
func (l *Line) releaseCall(c *Call, reason string) {
	l.mu.Lock()
	delete(l.calls, c.id)
	l.endedAt[c.id] = time.Now()
	if len(l.endedAt) > 4096 {
		cutoff := time.Now().Add(-time.Hour)
		for id, ts := range l.endedAt {
			if ts.Before(cutoff) {
				delete(l.endedAt, id)
			}
		}
	}
	if c.port != 0 {
		delete(l.ports, c.port)
	}
	l.mu.Unlock()

	l.emit(eventEnded, c, map[string]any{
		"call_id":     c.id,
		"reason":      reason,
		"duration_ms": int64(time.Since(c.startedAt) / time.Millisecond),
	})
}

// emit publishes one CloudEvents 1.0 entry to the events stream.
func (l *Line) emit(ceType string, c *Call, data map[string]any) {
	if _, ok := data["call_id"]; !ok {
		data["call_id"] = c.id
	}
	b, err := json.Marshal(data)
	if err != nil {
		l.log.Error("event data marshal failed", "type", ceType, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := sredis.XAddArgs(eventsStream, map[string]any{
		"ce_specversion":     "1.0",
		"ce_id":              uuid.NewString(),
		"ce_source":          ceSource,
		"ce_type":            ceType,
		"ce_subject":         c.id,
		"ce_time":            time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"ce_datacontenttype": "application/json",
		"data":               string(b),
	})
	if err := l.rdb.XAdd(ctx, args).Err(); err != nil {
		l.log.Warn("failed to publish event", "type", ceType, "call", c.id, "err", err)
	}
}

// Listen starts (or moves, or with to "" stops) the browser-mic RTP stream
// for a call toward a host:port.
func (l *Line) listen(callID, to string) error {
	l.mu.Lock()
	call, active := l.calls[callID]
	l.mu.Unlock()
	if !active {
		return fmt.Errorf("unknown_call: no active call with id %q", callID)
	}
	if to == "" {
		call.setDestination(nil)
		return nil
	}
	addr, err := net.ResolveUDPAddr("udp", to)
	if err != nil {
		return fmt.Errorf("bad_target: cannot resolve %q (%v)", to, err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fmt.Errorf("bad_target: cannot dial %q (%v)", to, err)
	}
	call.setDestination(conn)
	call.noteActivity()
	return nil
}

// Hangup ends an active call with local-hangup; an ended call returns ok
// without a second event.
func (l *Line) hangup(callID string) error {
	l.mu.Lock()
	call, active := l.calls[callID]
	if !active {
		_, wasEnded := l.endedAt[callID]
		l.mu.Unlock()
		if wasEnded {
			return nil
		}
		return fmt.Errorf("unknown_call: no active call with id %q", callID)
	}
	l.mu.Unlock()
	call.finish("local-hangup")
	return nil
}

type listenIn struct {
	CallID string `json:"call_id"`
	To     string `json:"to"`
}

type hangupIn struct {
	CallID string `json:"call_id"`
}

type okOut struct {
	Ok bool `json:"ok"`
}

func (l *Line) listenTool(_ context.Context, _ *mcp.CallToolRequest, in listenIn) (*mcp.CallToolResult, okOut, error) {
	if err := l.listen(in.CallID, in.To); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Ok: true}, nil
}

func (l *Line) hangupTool(_ context.Context, _ *mcp.CallToolRequest, in hangupIn) (*mcp.CallToolResult, okOut, error) {
	if err := l.hangup(in.CallID); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Ok: true}, nil
}
