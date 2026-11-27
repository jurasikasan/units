// Command body runs one body: its senses opened and served as its ears and
// eyes, its own perception republished onto its stream, its acts as MCP tools.
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
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/jurasikasan/units/body"
	sharedredis "github.com/jurasikasan/units/shared/redis"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	name := envStr("BODY_NAME", "")
	if name == "" {
		log.Fatal("body: BODY_NAME is required: it names the body's stream and source")
	}
	must(body.CheckName(name), "BODY_NAME")
	id := body.DerivedID(name)
	if v := envStr("BODY_ID", ""); v != "" {
		var err error
		id, err = uuid.Parse(v)
		must(err, "BODY_ID")
	}
	description := envStr("BODY_DESCRIPTION", "")
	if f := envStr("BODY_DESCRIPTION_FILE", ""); f != "" {
		if description != "" {
			log.Fatal("body: BODY_DESCRIPTION and BODY_DESCRIPTION_FILE are both set; one says what the body is")
		}
		raw, err := os.ReadFile(f)
		must(err, "BODY_DESCRIPTION_FILE")
		description = strings.TrimSpace(string(raw))
	}
	earshot, err := body.ParseEarshot(envStr("BODY_EARSHOT", ""))
	must(err, "BODY_EARSHOT")
	cfg := body.Config{
		ID: id, Name: name, Description: description,
		Stream:       envStr("BODY_STREAM", body.StreamFor(name)),
		Group:        envStr("BODY_GROUP", body.GroupFor(name)),
		HearingURL:   envStr("HEARING_MCP_URL", ""),
		SightURL:     envStr("SIGHT_MCP_URL", ""),
		SpeakingURL:  envStr("SPEAKING_MCP_URL", ""),
		Language:     envStr("BODY_LANGUAGE", ""),
		Voice:        envStr("BODY_VOICE", ""),
		Earshot:      earshot,
		SpeakTimeout: envDur("BODY_SPEAK_TIMEOUT", body.DefaultSpeakTimeout),
		Retry:        envDur("BODY_RETRY", body.DefaultRetry),
	}
	if cfg.SpeakingURL != "" && cfg.Voice == "" {
		log.Fatal("body: SPEAKING_MCP_URL is set and BODY_VOICE is not: speaking has no default voice")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rdb := sharedredis.GetRedisClient(ctx)
	b := body.New(cfg, rdb, &http.Client{Timeout: 10 * time.Second}, logger)
	logger.Info("body: up", "version", body.Version, "id", id.String(), "name", name, "stream", cfg.Stream,
		"group", cfg.Group, "hearing", cfg.HearingURL != "", "sight", cfg.SightURL != "", "mouth", cfg.SpeakingURL != "",
		"earshot", earshot)

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	mux := b.Handler()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": body.Version, "self": b.Self(), "counts": b.Counts.Snapshot()})
	})
	srv := &http.Server{Addr: ":" + strconv.Itoa(envInt("PORT", body.DefaultPort)), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("body: %v", err)
		}
	}()
	if err := <-done; err != nil {
		log.Fatalf("body: %v", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func must(err error, name string) {
	if err != nil {
		log.Fatalf("body: %s: %v", name, err)
	}
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// envInt and envDur refuse a malformed value: a body started on a knob it
// did not read is worse than one that did not start.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Fatalf("body: %s=%q is not a positive number", name, v)
	}
	return n
}

func envDur(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Fatalf("body: %s=%q is not a positive duration", name, v)
	}
	return d
}
