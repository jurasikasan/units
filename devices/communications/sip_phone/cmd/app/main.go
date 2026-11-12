// phone is a device: a telephone line that places and takes calls, carries
// audio into them and reports what happened (phone/CONTRACT.md). Tools over
// MCP on HTTP, SIP on UDP, events on phone_events.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/emiago/sipgo"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/call"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/device"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/mcpserver"

	sharedredis "github.com/jurasikasan/units/shared/redis"
)

var version = "dev"

func main() {
	cfg, listen, err := configFromEnv()
	if err != nil {
		log.Fatalf("phone: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("phone/1.0"))
	if err != nil {
		log.Fatalf("phone: UA: %v", err)
	}
	defer ua.Close() //nolint:errcheck
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		log.Fatalf("phone: SIP server: %v", err)
	}
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname(cfg.AdvertiseHost), sipgo.WithClientPort(cfg.SIPPort))
	if err != nil {
		log.Fatalf("phone: SIP client: %v", err)
	}

	rdb := sharedredis.GetRedisClient(ctx)
	pub := events.Redis(rdb)
	m := call.NewManager(cfg, cli, pub)
	srv.OnBye(m.OnBye)
	srv.OnInvite(m.OnInvite)
	srv.OnAck(m.OnAck)
	srv.OnOptions(m.OnOptions)
	srv.OnUpdate(m.OnUpdate)
	dev := device.New(m)

	tools := mcpserver.New(dev, version)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return tools }, nil))
	httpSrv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("phone: HTTP: %v", err)
		}
	}()

	log.Printf("phone %s: SIP on %s, trunk %s, advertising %s:%d, up to %d calls", version, listen, cfg.Trunk, cfg.AdvertiseHost, cfg.SIPPort, cfg.MaxCalls)
	if err := srv.ListenAndServe(ctx, "udp", listen); err != nil && ctx.Err() == nil {
		log.Fatalf("phone: SIP listen: %v", err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
	m.Wait()
}

func configFromEnv() (call.Config, string, error) {
	listen := env("SIP_LISTEN", "0.0.0.0:5070")
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return call.Config{}, "", fmt.Errorf("SIP_LISTEN %q: %w", listen, err)
	}
	port, _ := strconv.Atoi(portStr)

	cfg := call.Config{
		Trunk:          os.Getenv("SIP_TRUNK"),
		AdvertiseHost:  os.Getenv("SIP_ADVERTISE_ADDR"),
		SIPPort:        port,
		RTPPortMin:     envInt("RTP_PORT_MIN", 42000),
		RTPPortMax:     envInt("RTP_PORT_MAX", 42199),
		MaxCalls:       envInt("PHONE_MAX_CALLS", 4),
		RingDefault:    time.Duration(envInt("RING_TIMEOUT_DEFAULT_S", 30)) * time.Second,
		RingMax:        time.Duration(envInt("RING_TIMEOUT_MAX_S", 60)) * time.Second,
		Username:       os.Getenv("SIP_USERNAME"),
		Password:       os.Getenv("SIP_PASSWORD"),
		DefaultFrom:    os.Getenv("SIP_DEFAULT_FROM"),
		StripPlus:      os.Getenv("SIP_STRIP_PLUS") == "true",
		Prefix:         os.Getenv("SIP_NUMBER_PREFIX"),
		AudioInHost:    env("AUDIO_IN_HOST", "phone"),
		AudioInPortMin: envInt("AUDIO_IN_PORT_MIN", 43000),
		AudioInPortMax: envInt("AUDIO_IN_PORT_MAX", 43199),
		// Inbound (CONTRACT §6.1): a bad duration falls back to the contract default.
		InboundAnswerTimeout: envDuration("INBOUND_ANSWER_TIMEOUT", 30*time.Second),
		RTPIdleTimeout:       envDuration("RTP_IDLE_TIMEOUT", 60*time.Second),
	}
	if v := os.Getenv("SIP_FROM_ALLOW"); v != "" {
		cfg.FromAllow = strings.Split(v, ",")
	}
	if v := os.Getenv("SIP_INBOUND_ALLOW"); v != "" {
		for _, ip := range strings.Split(v, ",") {
			if ip = strings.TrimSpace(ip); ip != "" {
				cfg.InboundAllow = append(cfg.InboundAllow, ip)
			}
		}
	}
	if cfg.AdvertiseHost == "" {
		// The address this box uses toward the trunk: right on a flat network
		// and inside docker; behind NAT it must be set by hand. A
		// line with no trunk only takes calls; the route toward TEST-NET-1 is
		// a lookup, no packet leaves.
		toward := cfg.Trunk
		if toward == "" {
			toward = "192.0.2.1:5060"
		}
		c, err := net.Dial("udp", toward)
		if err != nil {
			return cfg, "", fmt.Errorf("SIP_ADVERTISE_ADDR unset and no route to %s: %w", toward, err)
		}
		cfg.AdvertiseHost = c.LocalAddr().(*net.UDPAddr).IP.String()
		_ = c.Close()
	}
	if net.ParseIP(cfg.AdvertiseHost) == nil {
		return cfg, "", fmt.Errorf("SIP_ADVERTISE_ADDR must be the IP the trunk reaches us on, got %q", cfg.AdvertiseHost)
	}
	return cfg, listen, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
