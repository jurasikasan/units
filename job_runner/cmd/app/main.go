// job_runner runs one script per call in a fresh container and answers when
// it has ended (CONTRACT.md). See internal/run.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
	"github.com/jurasikasan/units/job_runner/internal/run"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is stamped at build time.
var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	maxKeep, err := time.ParseDuration(env("MAX_KEEP", "4h"))
	if err != nil || maxKeep <= 0 {
		log.Fatalf("MAX_KEEP: want a positive Go duration (4h, 30m)")
	}
	// SECRET_<NAME>=v is the secret NAME (CONTRACT.md).
	secrets := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if n, ok := strings.CutPrefix(name, "SECRET_"); ok && n != "" {
			secrets[n] = value
		}
	}
	runner := &run.Runner{
		Secrets:      secrets,
		HelpersDir:   os.Getenv("HELPERS_DIR"),
		Docker:       engine.New(env("DOCKER_HOST", "unix:///var/run/dind/docker.sock")),
		DefaultImage: env("DEFAULT_IMAGE", "bash:5.2"),
		MaxKeep:      maxKeep,
		Minute:       time.Minute,
		Now:          time.Now,
	}
	go runner.Watch(ctx, 30*time.Second)

	server := run.NewMCPServer(runner, version)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
	})
	mux.Handle("/", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))

	// No write timeout: a run call is answered when its run ends.
	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("job_runner %s listening on %s", version, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
