// Command sight runs the sight sense: MCP tools open/close over streamable
// HTTP (stateless), RTP video in on video_in, SenML packs in on sight_in,
// frames kept in artifact_store, events on Redis.
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

	"github.com/jurasikasan/units/senses/sight"
)

// version is stamped at build time (-ldflags "-X main.version=$VERSION").
var version = "dev"

func main() {
	port := envInt("PORT", sight.DefaultPort)
	visionTimeout := envDur("SIGHT_VISION_TIMEOUT", sight.DefaultVisionTimeout)
	cfg := sight.Config{
		VisionURL:       envStr("VISION_URL", sight.DefaultVisionURL),
		VisionKey:       envStr("VISION_KEY", ""),
		VisionModel:     envStr("VISION_MODEL", sight.DefaultVisionModel),
		VisionTimeout:   visionTimeout,
		ChangeThreshold: envFloat("SIGHT_CHANGE_THRESHOLD", sight.DefaultChangeThreshold),
		DescribeEvery:   envDur("SIGHT_DESCRIBE_EVERY", sight.DefaultDescribeEvery),
		MaxSessions:     envInt("SIGHT_MAX_SESSIONS", sight.DefaultMaxSessions),
		IdleTimeout:     envDur("SIGHT_IDLE_TIMEOUT", sight.DefaultIdleTimeout),
		InURL:           envStr("SIGHT_IN_URL", sight.DefaultInURL),
		SampleEvery:     envDur("SIGHT_SAMPLE_EVERY", sight.DefaultSampleEvery),
		VideoInHost:     envStr("VIDEO_IN_HOST", sight.DefaultVideoInHost),
		PortMin:         envInt("VIDEO_IN_PORT_MIN", sight.DefaultPortMin),
		PortMax:         envInt("VIDEO_IN_PORT_MAX", sight.DefaultPortMax),
		FFmpeg:          envStr("SIGHT_FFMPEG", sight.DefaultFFmpeg),
		ArtifactURL:     envStr("ARTIFACT_URL", sight.DefaultArtifactURL),
		ArtifactSpace:   envStr("SIGHT_ARTIFACT_SPACE", sight.DefaultArtifactSpace),
	}
	// Create the sink first: a Redis outage fails the service fast.
	cfg.Sink = sight.NewRedisSink(context.Background())
	svc := sight.NewService(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	})
	srv := sight.NewMCPServer(svc, version)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	mux.Handle("POST /in/{session_id}", svc.InputHandler())

	addr := ":" + strconv.Itoa(port)
	log.Printf("sight: listening on %s (MCP on /mcp, sight_in on POST /in/{session_id}, health on GET /health)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("sight: %v", err)
	}
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
		log.Printf("sight: bad %s=%q, using %d", name, v, def)
	}
	return def
}

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		log.Printf("sight: bad %s=%q, using %v", name, v, def)
	}
	return def
}

func envDur(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("sight: bad %s=%q, using %s", name, v, def)
	}
	return def
}
