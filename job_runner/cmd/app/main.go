// job_runner runs one script per call in a fresh, confined container, with
// the files the call brings, and answers when it has ended (CONTRACT.md).
// See internal/run.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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

// pairs reads name=value,name=value, keeping the order.
func pairs(name, v string) ([][2]string, error) {
	var out [][2]string
	seen := map[string]bool{}
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		k, val, ok := strings.Cut(item, "=")
		k, val = strings.TrimSpace(k), strings.TrimSpace(val)
		if !ok || k == "" || val == "" {
			return nil, fmt.Errorf("%s: %q is not name=value", name, item)
		}
		if seen[k] {
			return nil, fmt.Errorf("%s: %q twice", name, k)
		}
		seen[k] = true
		out = append(out, [2]string{k, val})
	}
	return out, nil
}

// bytesOf reads 512m, 2g, or plain bytes.
func bytesOf(v string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "g"):
		mult, v = 1<<30, strings.TrimSuffix(v, "g")
	case strings.HasSuffix(v, "m"):
		mult, v = 1<<20, strings.TrimSuffix(v, "m")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("want a positive size like 512m or 2g")
	}
	return n * mult, nil
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
	imgs, err := pairs("IMAGES", os.Getenv("IMAGES"))
	if err != nil || len(imgs) == 0 {
		log.Fatalf("IMAGES: want name=reference,… with at least one (%v)", err)
	}
	var images []run.Image
	for _, p := range imgs {
		images = append(images, run.Image{Name: p[0], Ref: p[1]})
	}
	nets, err := pairs("NETWORKS", os.Getenv("NETWORKS"))
	if err != nil {
		log.Fatal(err)
	}
	networks := map[string]string{}
	for _, p := range nets {
		if p[0] == run.NetworkNone || p[0] == run.NetworkInternet {
			log.Fatalf("NETWORKS: %q is built in", p[0])
		}
		networks[p[0]] = p[1]
	}
	memory, err := bytesOf(env("RUN_MEMORY", "2g"))
	if err != nil {
		log.Fatalf("RUN_MEMORY: %v", err)
	}
	pids, err := strconv.ParseInt(env("RUN_PIDS", "512"), 10, 64)
	if err != nil || pids <= 0 {
		log.Fatalf("RUN_PIDS: want a positive number")
	}
	cpus, err := strconv.ParseFloat(env("RUN_CPUS", "2"), 64)
	if err != nil || cpus <= 0 {
		log.Fatalf("RUN_CPUS: want a positive number")
	}
	var transferURLs []string
	for _, u := range strings.Split(os.Getenv("TRANSFER_URLS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			transferURLs = append(transferURLs, u)
		}
	}
	runner := &run.Runner{
		Secrets:      secrets,
		HelpersDir:   os.Getenv("HELPERS_DIR"),
		Docker:       engine.New(env("DOCKER_HOST", "unix:///var/run/dind/docker.sock")),
		Images:       images,
		Networks:     networks,
		Limits:       run.Limits{Memory: memory, Pids: pids, NanoCPUs: int64(cpus * 1e9)},
		Runtime:      "runsc",
		TransferURLs: transferURLs,
		TransferTime: 10 * time.Minute,
		HTTP:         &http.Client{},
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
