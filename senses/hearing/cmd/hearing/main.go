// Command hearing runs the hearing organ (C5): MCP tools open/close
// over streamable HTTP, RTP/UDP audio in, events on Redis.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/senses/hearing"
)

func main() {
	port := envInt("PORT", hearing.DefaultPort)
	cfg := hearing.Config{
		STTURL:          envStr("STT_URL", hearing.DefaultSTTURL),
		STTKey:          envStr("STT_KEY", ""),
		STTModel:        envStr("STT_MODEL", hearing.DefaultSTTModel),
		VADKind:         envStr("HEARING_VAD", hearing.DefaultVADKind),
		VADURL:          envStr("VAD_URL", hearing.DefaultVADURL),
		EnergyThreshold: envFloat("HEARING_ENERGY_THRESHOLD", hearing.DefaultEnergyThreshold),
		Onset:           envDur("HEARING_ONSET", hearing.DefaultOnset),
		MergeWindow:     envDur("HEARING_MERGE_WINDOW", hearing.DefaultMergeWindow),
		PreRoll:         envDur("HEARING_PREROLL", hearing.DefaultPreRoll),
		ShortWords:      envStr("HEARING_SHORT", hearing.DefaultShortWords),
		MaxUtterance:    envDur("HEARING_MAX_UTTERANCE", hearing.DefaultMaxUtterance),
		MaxSessions:     envInt("HEARING_MAX_SESSIONS", hearing.DefaultMaxSessions),
		IdleTimeout:     envDur("HEARING_IDLE_TIMEOUT", hearing.DefaultIdleTimeout),
		AudioInHost:     envStr("AUDIO_IN_HOST", hearing.DefaultAudioInHost),
		PortMin:         envInt("AUDIO_IN_PORT_MIN", hearing.DefaultPortMin),
		PortMax:         envInt("AUDIO_IN_PORT_MAX", hearing.DefaultPortMax),
	}
	// Create the sink first: a Redis outage fails the service fast.
	svc := hearing.NewService(appendSink(cfg, hearing.NewRedisSink(context.Background())))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	mcpSrv := hearing.NewMCPServer(svc)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpSrv
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))

	addr := ":" + strconv.Itoa(port)
	log.Printf("hearing: listening on %s (health on GET /health, MCP on /mcp)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("hearing: %v", err)
	}
}

func appendSink(cfg hearing.Config, sink hearing.Sink) hearing.Config {
	cfg.Sink = sink
	return cfg
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("hearing: bad %s=%q, using %d", name, v, def)
	}
	return def
}

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		log.Printf("hearing: bad %s=%q, using %v", name, v, def)
	}
	return def
}

func envDur(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("hearing: bad %s=%q, using %s", name, v, def)
	}
	return def
}
