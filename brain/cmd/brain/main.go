// Command brain runs the first brain: the senses' events in from Redis
// streams, thoughts out to the log and to its thread in threads.
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
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/jurasikasan/units/brain"
	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// version is stamped at build time (-ldflags "-X main.version=$VERSION").
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	senses := brain.ParseList(os.Getenv("BRAIN_SENSES"))
	if len(senses) == 0 {
		log.Fatal("brain: BRAIN_SENSES names no stream: the streams of the senses to read, e.g. hearing_events,sight_events")
	}
	closed, err := brain.ParseGate(envStr("BRAIN_GATE", ""))
	must(err, "BRAIN_GATE")
	keep, keepDefault, err := brain.ParseKeep(envStr("BRAIN_KEEP", ""), brain.DefaultKeep)
	must(err, "BRAIN_KEEP")
	senders, err := brain.ParseSenders(envStr("BRAIN_SENDERS", ""))
	must(err, "BRAIN_SENDERS")
	self := brain.DerivedSubject("self")
	if v := envStr("BRAIN_SUBJECT", ""); v != "" {
		self, err = uuid.Parse(v)
		must(err, "BRAIN_SUBJECT")
	}
	prompt := brain.DefaultPrompt
	if f := envStr("BRAIN_PROMPT_FILE", ""); f != "" {
		raw, err := os.ReadFile(f)
		must(err, "BRAIN_PROMPT_FILE")
		prompt = string(raw)
	}
	thread := envStr("BRAIN_THREAD", "")
	if thread != "" {
		_, err := uuid.Parse(thread)
		must(err, "BRAIN_THREAD")
	}

	cfg := brain.Config{
		Closed: closed, Keep: keep, KeepDefault: keepDefault,
		BufferMax:    envInt("BRAIN_BUFFER_MAX", brain.DefaultBufferMax),
		Settle:       envDur("BRAIN_SETTLE", brain.DefaultSettle),
		MaxWait:      envDur("BRAIN_MAX_WAIT", brain.DefaultMaxWait),
		Bind:         envDur("BRAIN_BIND", brain.DefaultBind),
		Recall:       envInt("BRAIN_RECALL", brain.DefaultRecall),
		Prompt:       prompt,
		Self:         self,
		Senders:      senders,
		ModelTimeout: envDur("BRAIN_MODEL_TIMEOUT", brain.DefaultModelTimeout),
		ActTimeout:   envDur("BRAIN_ACT_TIMEOUT", brain.DefaultActTimeout),
	}
	if cfg.Recall > 1000 {
		log.Fatal("brain: BRAIN_RECALL is at most 1000, threads' page")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	// Without a body the brain thinks and does nothing (CONTRACT §2.5). An
	// act may take as long as the body takes to do it: the act's own timeout
	// bounds it, not the client's.
	var body *brain.Body
	if u := envStr("BRAIN_BODY", ""); u != "" {
		body = &brain.Body{URL: u, HTTP: &http.Client{}}
	}
	counts := &brain.Counts{}
	memory := &brain.Memory{URL: envStr("THREADS_URL", brain.DefaultThreadsURL), Client: client, Self: self, Thread: thread}
	mind := &brain.Mind{
		Cfg:    cfg,
		Model:  brain.Model{URL: envStr("MODEL_URL", brain.DefaultModelURL), Key: envStr("MODEL_KEY", ""), Name: envStr("MODEL", brain.DefaultModel), Client: &http.Client{}},
		Store:  brain.Store{URL: envStr("ARTIFACT_URL", brain.DefaultArtifactURL), Client: client},
		Memory: memory,
		Body:   body,
		Counts: counts,
		Log:    logger,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rdb := sharedredis.GetRedisClient(ctx)
	streams := brain.Streams{Client: rdb, Names: senses, Group: envStr("BRAIN_GROUP", brain.DefaultGroup), Consumer: "brain"}
	if err := streams.Ensure(ctx); err != nil {
		log.Fatalf("brain: %v", err)
	}
	logger.Info("brain: up", "version", version, "senses", senses, "group", streams.Group, "self", self.String(),
		"keep_default", keepDefault.String(), "settle", cfg.Settle.String(), "max_wait", cfg.MaxWait.String(),
		"bind", cfg.Bind.String(), "recall", cfg.Recall, "thread", thread, "body", body != nil)
	if id, opened, err := memory.Resolve(ctx); err != nil {
		logger.Warn("memory: no thread yet, trying again at each thought", "err", err.Error())
	} else {
		logger.Info("memory: thread", "thread", id, "opened", opened)
	}

	events := make(chan brain.Event, 64)
	go streams.Run(ctx, events, counts, logger)
	go mind.Run(ctx, events)

	port := envInt("PORT", brain.DefaultPort)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "version": version, "buffered": mind.Buffered(), "counts": counts.Snapshot(),
		})
	})
	srv := &http.Server{Addr: ":" + strconv.Itoa(port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
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
