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
	"github.com/jurasikasan/units/reins/internal/holds"
	"github.com/jurasikasan/units/reins/internal/stewardship"
	"github.com/jurasikasan/units/reins/internal/world"

	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
)

// @title          Reins
// @version        1.0
// @description    Where a principal holds the reins of its subjects: which
// @description    subjects an account at the door may look into and take up.
// @description    A principal first seen is given two personas in `subjects`.
// @description    Who holds each subject's reins now (the one wheel): taken
// @description    from a device, handed to a brain, given back.
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

	// The bus, where REDIS_ADDR names it: threads' wake-ups and the writing
	// streams followed for /me/stream, reins_events published. Redis is not
	// waited for: a read that fails ends one stream, and the reader comes
	// back; an event that cannot be put is logged.
	var rdb *redis.Client
	var bus holds.Publisher
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		// Each stream holds one connection while it waits on the bus.
		rdb = redis.NewClient(&redis.Options{Addr: addr, PoolSize: 500})
		bus = holds.RedisBus{Client: rdb}
	} else {
		log.Println("no REDIS_ADDR: /me/stream answers 503, holds are not announced")
	}
	var brains holds.Brains
	if u := os.Getenv("BRAINS_URL"); u != "" {
		brains = holds.NewHTTPBrains(u)
	} else {
		log.Println("no BRAINS_URL: no brains to hand an avatar to")
	}
	hs := holds.NewService(holds.NewPGXStore(pool), repo, bus, brains)

	grace, err := time.ParseDuration(envOr("HOLD_GRACE", "2m"))
	if err != nil || grace <= 0 {
		log.Fatal("HOLD_GRACE is a duration, e.g. 2m")
	}
	go hs.Lapsing(ctx, grace, min(grace/4, 15*time.Second))

	wsvc, wh := worldHandler(repo, hs, rdb)
	defaultBrain := os.Getenv("DEFAULT_BRAIN")
	svc.Welcome = func(ctx context.Context, principal string, login, assistant uuid.UUID) error {
		if defaultBrain != "" {
			if err := hs.Default(ctx, assistant, defaultBrain); err != nil {
				return err
			}
		}
		if wsvc != nil {
			return wsvc.FirstThread(ctx, assistant, login)
		}
		return nil
	}
	r := newRouter(stewardship.NewHandler(svc, hs), wh, holds.NewHandler(hs), pool)

	// The inner port: what a driver reads (whom its profiles hold). The door
	// forwards only the outer one.
	inner := mux.NewRouter()
	holds.RegisterInternal(inner, holds.NewHandler(hs))
	internalPort := envOr("INTERNAL_PORT", "8088")
	go func() {
		srv := &http.Server{Addr: ":" + internalPort, Handler: inner, ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
	}()

	log.Printf("reins listening on :%s, inner :%s", port, internalPort)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// worldHandler is the face's door into the world: threads where THREADS_URL
// names them, followed on the bus (threads_events, the streams in
// WRITING_STREAMS for replies being written, reins_events for who holds
// what). Without THREADS_URL its routes answer 503, without a bus the
// stream does.
func worldHandler(stewards world.Stewards, hs *holds.Service, rdb *redis.Client) (*world.Service, *world.Handler) {
	threadsURL := os.Getenv("THREADS_URL")
	if threadsURL == "" {
		log.Println("no THREADS_URL: the world routes answer 503")
		return nil, world.NewHandler(nil, nil)
	}
	svc := world.NewService(stewards, world.NewHTTPJournal(threadsURL), hs)
	if rdb == nil {
		return svc, world.NewHandler(svc, nil)
	}
	writing := []string{}
	for _, s := range strings.Split(envOr("WRITING_STREAMS", "driver_events"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			writing = append(writing, s)
		}
	}
	st := world.NewStream(svc, rdb, writing)
	st.Presence = hs
	return svc, world.NewHandler(svc, st)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// newRouter is the whole routing table, so that routes_documented_test.go
// checks the router the server actually serves.
func newRouter(h *stewardship.Handler, wh *world.Handler, hh *holds.Handler, db pinger) *mux.Router {
	r := mux.NewRouter()
	r.Use(httpHelper.LoggingMiddleware)
	stewardship.RegisterRoutes(r, h)
	world.RegisterRoutes(r, wh)
	holds.RegisterRoutes(r, hh)
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
