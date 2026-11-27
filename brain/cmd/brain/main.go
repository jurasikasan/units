// Command brain runs the first brain: the senses' events in from Redis
// streams, thoughts out to the log and to its thread in threads.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jurasikasan/units/brain"
	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// version is stamped at build time (-ldflags "-X main.version=$VERSION").
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	prompt := ""
	if f := envStr("BRAIN_PROMPT_FILE", ""); f != "" {
		raw, err := os.ReadFile(f)
		must(err, "BRAIN_PROMPT_FILE")
		prompt = string(raw)
	}
	hc := brain.HostConfig{
		ThreadsURL:   envStr("THREADS_URL", brain.DefaultThreadsURL),
		ArtifactURL:  envStr("ARTIFACT_URL", brain.DefaultArtifactURL),
		Prompt:       prompt,
		ModelTimeout: envDur("BRAIN_MODEL_TIMEOUT", brain.DefaultModelTimeout),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rdb := sharedredis.GetRedisClient(ctx)
	h := brain.NewHost(hc, rdb, logger)
	h.Start(ctx)

	// A brain named by the env (BRAIN_SENSES) is made at start, as a brain
	// run alone always was: its group BRAIN_GROUP, its subject BRAIN_SUBJECT,
	// its policy the env's model. Without one, the host carries only what
	// is PUT.
	if senses := brain.ParseList(os.Getenv("BRAIN_SENSES")); len(senses) > 0 {
		name := envStr("BRAIN_NAME", "self")
		spec := brain.Spec{
			Subject: envStr("BRAIN_SUBJECT", ""), Senses: senses, Thread: envStr("BRAIN_THREAD", ""),
			Gate: envStr("BRAIN_GATE", ""), Keep: envStr("BRAIN_KEEP", ""), Senders: envStr("BRAIN_SENDERS", ""),
			BufferMax: envInt("BRAIN_BUFFER_MAX", brain.DefaultBufferMax),
			Settle:    envStr("BRAIN_SETTLE", ""), MaxWait: envStr("BRAIN_MAX_WAIT", ""), Bind: envStr("BRAIN_BIND", ""),
			ActTimeout: envStr("BRAIN_ACT_TIMEOUT", ""),
			Policy: &brain.PolicySpec{ModelURL: envStr("MODEL_URL", brain.DefaultModelURL),
				Model: envStr("MODEL", brain.DefaultModel), Key: envStr("MODEL_KEY", "")},
		}
		recall := envInt("BRAIN_RECALL", brain.DefaultRecall)
		spec.Recall = &recall
		bodyMCP, group := envStr("BRAIN_BODY", ""), envStr("BRAIN_GROUP", brain.DefaultGroup)
		h.Default = name
		h.Overrides = func(b *brain.Built) { b.BodyMCP, b.Group = bodyMCP, group }
		l, _, err := h.Put(ctx, name, spec)
		must(err, "the brain of the env")
		logger.Info("brain: the env's brain made", "name", name, "senses", senses, "group", group,
			"subject", l.Subject, "body", bodyMCP != "")
	}
	logger.Info("brain: up", "version", version, "default", h.Default)

	port := envInt("PORT", brain.DefaultPort)
	srv := &http.Server{Addr: ":" + strconv.Itoa(port), Handler: h.Handler(version), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		h.Stop()
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("brain: %v", err)
	}
}

func must(err error, name string) {
	if err != nil {
		log.Fatalf("brain: %s: %v", name, err)
	}
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// envInt and envDur refuse a malformed value: a brain started on a knob it
// did not read is worse than one that did not start.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Fatalf("brain: %s=%q is not a count", name, v)
	}
	return n
}

func envDur(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Fatalf("brain: %s=%q is not a duration", name, v)
	}
	return d
}
