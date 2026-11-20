// drivers/model: a chat model holding subjects' reins, as one of its
// profiles, when reins says so. Woken by threads' events when someone else
// says something in a thread its subject is in,
// it builds the prompt from the thread's tail, has minds/assistant answer
// as an AG-UI run with the model it holds the subject with, streams the
// answer's pieces on driver_events, and says the answer in the thread as
// the subject.
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

	"github.com/jurasikasan/units/drivers/model/internal/driver"

	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// version is stamped at build time (-ldflags "-X main.version=$VERSION").
var version = "dev"

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	profiles, err := driver.ParseProfiles(mustEnv("PROFILES"))
	if err != nil {
		log.Fatal(err)
	}
	reinsURL := mustEnv("REINS_URL")
	tail, err := strconv.Atoi(envOr("TAIL", "50"))
	if err != nil || tail < 1 || tail > 1000 {
		log.Fatal("TAIL is 1…1000")
	}
	threadsURL := mustEnv("THREADS_URL")
	subjectsURL := mustEnv("SUBJECTS_URL")
	mindURL := mustEnv("MIND_URL")
	catchup, err := time.ParseDuration(envOr("CATCHUP", "2m"))
	if err != nil {
		log.Fatal("CATCHUP is a duration, e.g. 2m")
	}
	group := envOr("GROUP", "drivers-model")
	consumer, _ := os.Hostname()
	port := envOr("PORT", "8097")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rdb := sharedredis.GetRedisClient(ctx)

	d := &driver.Driver{
		Journal:  driver.NewHTTPThreads(threadsURL),
		Names:    driver.NewHTTPSubjects(subjectsURL),
		Mind:     driver.NewAGUI(mindURL, "drivers/model"),
		Bus:      driver.RedisPublisher{Client: rdb},
		Reins:    driver.NewHTTPReins(reinsURL),
		Profiles: profiles,
		Tail:     tail,
		Catchup:  catchup,
		Log:      logger,
	}
	go d.FollowReins(ctx, rdb, 30*time.Second)

	mux := http.NewServeMux()
	// The brains it can be, for reins to relay: a profile's name and its
	// chat model.
	mux.HandleFunc("GET /profiles", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(profiles)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
	})
	// Ready when the bus answers; holding nobody is not a failure (reins
	// says whom its profiles hold), and /ready says whom it holds.
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		c, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := rdb.Ping(c).Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable", "error": err.Error()})
			return
		}
		held := map[string]string{}
		for _, h := range d.Holds() {
			held[h.Subject.Nick] = h.Profile
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "holds": held})
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	logger.Info("driver following threads_events", "group", group, "consumer", consumer, "tail", tail)
	if err := d.Follow(ctx, rdb, group, consumer); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
