// minds/realtime: a call in OpenAI's Realtime schema, made of organs —
// hearing, a model through an OpenAI-shaped chat endpoint, speaking. See
// README.md and contracts/edges.md, "Realtime, event by event".
package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	sharedredis "github.com/jurasikasan/units/shared/redis"

	"github.com/jurasikasan/units/minds/realtime/internal/audio"
	"github.com/jurasikasan/units/minds/realtime/internal/call"
	"github.com/jurasikasan/units/minds/realtime/internal/llm"
	"github.com/jurasikasan/units/minds/realtime/internal/organs"
)

var version = "dev"

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v, err := strconv.Atoi(env(name, strconv.Itoa(def)))
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return v
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	llmURL, llmModel, voice := os.Getenv("LLM_URL"), os.Getenv("LLM_MODEL"), os.Getenv("VOICE")
	if llmURL == "" || llmModel == "" || voice == "" {
		log.Fatal("LLM_URL, LLM_MODEL and VOICE are required")
	}
	rdb := sharedredis.GetRedisClient(ctx)
	o := &call.Organs{
		Hearing:    organs.New("hearing", env("HEARING_URL", "http://hearing:8080/mcp"), version),
		Speaking:   organs.New("speaking", env("SPEAKING_URL", "http://speaking:8080/"), version),
		HearingEv:  organs.Follow(ctx, rdb, "hearing_events"),
		SpeakingEv: organs.Follow(ctx, rdb, "speech_events"),
		LLM:        &llm.Client{Base: llmURL, Key: os.Getenv("LLM_KEY"), HTTP: &http.Client{Timeout: 5 * time.Minute}},
		Ports:      audio.NewPorts(envInt("AUDIO_IN_PORT_MIN", 46000), envInt("AUDIO_IN_PORT_MAX", 46099)),
		AudioHost:  env("AUDIO_IN_HOST", "realtime"),
	}
	d := call.Defaults{Model: llmModel, Voice: voice, Language: os.Getenv("SPEECH_LANGUAGE"), Instructions: os.Getenv("SYSTEM_PROMPT")}
	maxCalls := int64(envInt("MAX_CALLS", 4))
	var calls atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": version, "calls": calls.Load()})
	})
	mux.HandleFunc("GET /v1/realtime", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > maxCalls {
			calls.Add(-1)
			http.Error(w, `{"error":{"message":"capacity: too many calls","type":"server_error"}}`, http.StatusServiceUnavailable)
			return
		}
		defer calls.Add(-1)
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ws.SetReadLimit(16 << 20)
		model := r.URL.Query().Get("model")
		call.Serve(ctx, o, d, ws, model, logger)
		_ = ws.Close(websocket.StatusNormalClosure, "")
	})

	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logger.Info("minds/realtime", "version", version, "addr", srv.Addr, "llm_model", llmModel, "voice", voice)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
