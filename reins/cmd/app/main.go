package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jurasikasan/units/reins/internal/db"
	"github.com/jurasikasan/units/reins/internal/stewardship"
	"github.com/jurasikasan/units/reins/internal/world"

	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
)

// @title          Reins
// @version        1.0
// @description    Where a principal holds the reins of its subjects: which
// @description    subjects an account at the door may look into and take up.
// @description    A principal first seen is given two personas in `subjects`.
// @description    Reached only through the door (X-Principal-Id). Own database
// @description    on its own network.
// @host           localhost:8087
// @schemes        http
func main() {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	subjectsURL := os.Getenv("SUBJECTS_URL")
	if subjectsURL == "" {
		log.Fatal("SUBJECTS_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8087"
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	// Ping before migrating: pgxpool.New does not dial, so without this the
	// first error surfaces from inside Migrate and reads like a schema problem.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		log.Fatalf("postgres unreachable: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("schema applied")

	repo := stewardship.NewPGXRepository(pool)
	svc := stewardship.NewService(repo, stewardship.NewHTTPRegistry(subjectsURL))
	r := newRouter(stewardship.NewHandler(svc), worldHandler(repo), pool)

	log.Printf("reins listening on :%s", port)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// worldHandler is the face's door into the world: threads where THREADS_URL
// names them, followed on the bus where REDIS_ADDR names it (threads_events,
// and the streams in WRITING_STREAMS for replies being written). Without
// THREADS_URL its routes answer 503, without REDIS_ADDR the stream does.
// Redis is not waited for: a read that fails ends one stream, and the
// reader comes back.
func worldHandler(stewards world.Stewards) *world.Handler {
	threadsURL := os.Getenv("THREADS_URL")
	if threadsURL == "" {
		log.Println("no THREADS_URL: the world routes answer 503")
		return world.NewHandler(nil, nil)
	}
	svc := world.NewService(stewards, world.NewHTTPJournal(threadsURL))
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		log.Println("no REDIS_ADDR: /me/stream answers 503")
		return world.NewHandler(svc, nil)
	}
	writing := []string{}
	for _, s := range strings.Split(envOr("WRITING_STREAMS", "driver_events"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			writing = append(writing, s)
		}
	}
	// Each stream holds one connection while it waits on the bus.
	rdb := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 500})
	return world.NewHandler(svc, world.NewStream(svc, rdb, writing))
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// newRouter is the whole routing table, so that routes_documented_test.go
// checks the router the server actually serves.
func newRouter(h *stewardship.Handler, wh *world.Handler, db pinger) *mux.Router {
	r := mux.NewRouter()
	r.Use(httpHelper.LoggingMiddleware)
	stewardship.RegisterRoutes(r, h)
	world.RegisterRoutes(r, wh)
	r.HandleFunc("/health", healthHandler).Methods(http.MethodGet)
	r.HandleFunc("/ready", readyHandler(db)).Methods(http.MethodGet)
	return r
}

// version is stamped at build time (-ldflags "-X main.version=$VERSION",
// scripts/build.sh); "dev" means nobody stamped it.
var version = "dev"

// healthHandler godoc
// @Summary  Liveness probe and build version
// @Description  Answers if the process is serving HTTP at all, and says nothing
// @Description  about Postgres or subjects; scripts/stack_version.py reads the version.
// @Tags     health
// @Produce  json
// @Success  200 {object} map[string]string "status and version"
// @Router   /health [get]
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
}

// pinger is the one thing /ready needs from the pool; *pgxpool.Pool satisfies it.
type pinger interface {
	Ping(ctx context.Context) error
}

// readyTimeout must stay under the compose healthcheck timeout.
const readyTimeout = 2 * time.Second

// readyHandler godoc
// @Summary  Readiness probe: can this instance reach its Postgres
// @Description  200 when a ping to Postgres succeeds, 503 when it does not;
// @Description  the compose healthcheck asks this one.
// @Tags     health
// @Produce  json
// @Success  200 {object} map[string]string "status ok"
// @Failure  503 {object} map[string]string "status unavailable and the ping error"
// @Router   /ready [get]
func readyHandler(db pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable", "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
