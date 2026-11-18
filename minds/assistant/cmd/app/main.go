// minds/assistant: a chat assistant. AG-UI in, a model's answer streamed out;
// OpenAI's audio API handed on to the same endpoint.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jurasikasan/units/minds/assistant/internal/assistant"
)

var version = "dev"

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		slog.Error("missing configuration", "env", name)
		os.Exit(1)
	}
	return v
}

func main() {
	a := assistant.New(mustEnv("LLM_URL"), mustEnv("LLM_MODEL"), env("LLM_API_KEY", ""), env("SYSTEM_PROMPT", ""))
	servers, err := assistant.ParseServers(env("MCP_SERVERS", ""))
	if err != nil {
		slog.Error("MCP_SERVERS", "err", err)
		os.Exit(1)
	}
	steps, err := strconv.Atoi(env("MAX_TOOL_STEPS", "0"))
	if err != nil {
		slog.Error("MAX_TOOL_STEPS is not a number", "err", err)
		os.Exit(1)
	}
	idle, err := time.ParseDuration(env("LLM_IDLE_TIMEOUT", "0s"))
	if err != nil {
		slog.Error("LLM_IDLE_TIMEOUT is not a duration (e.g. 90s, 5m)", "err", err)
		os.Exit(1)
	}
	a.SetIdle(idle)
	if len(servers) > 0 {
		a.UseTools(servers, steps)
		slog.Info("tools from MCP servers", "servers", servers)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /run", a)
	stt, tts := env("STT_MODEL", ""), env("TTS_MODEL", "")
	assistant.NewAudio(mustEnv("LLM_URL"), env("LLM_API_KEY", ""), stt, tts, env("TTS_VOICE", "")).Routes(mux)
	if stt != "" || tts != "" {
		slog.Info("OpenAI's audio API handed on", "stt", stt, "tts", tts)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
	})

	addr := ":" + env("PORT", "8088")
	slog.Info("minds/assistant listening", "addr", addr, "version", version)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
