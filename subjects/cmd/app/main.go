package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jurasikasan/units/subjects/internal/db"
	"github.com/jurasikasan/units/subjects/internal/subject"

	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
)

// @title          Subjects
// @version        1.0
// @description    The registry of subjects: who exists, how they are addressed
// @description    (nick), what they are and their preferences, a JSON object
// @description    stored as it came. Own database on its own network.
// @host           localhost:8083
// @schemes        http
func main() {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
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

	svc := subject.NewDefaultSubjectService(subject.NewPGXSubjectRepository(pool))
	r := newRouter(subject.NewSubjectHandler(svc), pool)

	log.Printf("subjects listening on :%s", port)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// newRouter is the whole routing table, so that routes_documented_test.go
// checks the router the server actually serves.
func newRouter(h *subject.SubjectHandler, db pinger) *mux.Router {
	r := mux.NewRouter()
	r.Use(httpHelper.LoggingMiddleware)
	subject.RegisterRoutes(r, h)
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
// @Description  about Postgres; scripts/stack_version.py reads the version.
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
// @Summary  Readiness probe: can this instance reach Postgres
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
