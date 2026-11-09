// Command app is the speech service: the open/say/close MCP tools over
// streamable HTTP on PORT, the RTP audio out to the phone, and the
// speech_events stream. It asks TTS_URL's /audio/speech (LiteLLM) for synthesis (C4 §1, §5).
package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jurasikasan/units/abilities/speaking/internal/events"
	"github.com/jurasikasan/units/abilities/speaking/internal/speech"
	"github.com/jurasikasan/units/abilities/speaking/internal/tts"

	sharedredis "github.com/jurasikasan/units/shared/redis"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is stamped at build time (Dockerfile -X main.version=...).
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	// An OpenAI-shaped audio base: a router such as LiteLLM, with its
	// key in TTS_KEY, or one engine with none.
	ttsURL := envOr("TTS_URL", "http://litellm:4000/v1")
	ttsKey := os.Getenv("TTS_KEY")
	port := envOr("PORT", "8080")

	maxSessions := envInt("SPEECH_MAX_SESSIONS", 4)
	idleTimeout, err := time.ParseDuration(envOr("SPEECH_IDLE_TIMEOUT", "2m"))
	if err != nil || idleTimeout <= 0 {
		log.Fatalf("speech: bad SPEECH_IDLE_TIMEOUT %q", os.Getenv("SPEECH_IDLE_TIMEOUT"))
	}

	// REDIS_ADDR is read by shared/redis; it waits for the broker before we
	// start serving, because every event has to land somewhere.
	client := sharedredis.GetRedisClient(context.Background())
	pub := events.Redis{Client: client}

	sp := speech.New(speech.Config{MaxSessions: maxSessions, IdleTimeout: idleTimeout},
		tts.New(ttsURL, ttsKey), pub)

	srv := speech.Server(sp)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.Handle("/", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))

	log.Printf("speech %s: mcp on :%s, tts at %s", version, port, ttsURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Fatalf("speech: bad %s %q", key, v)
	}
	return n
}
