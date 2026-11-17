// minds/assistant: a chat assistant. AG-UI in, a model's answer streamed out.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

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

	mux := http.NewServeMux()
	mux.Handle("POST /run", a)
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
