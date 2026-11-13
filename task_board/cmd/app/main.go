package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jurasikasan/units/task_board/internal/db"
	"github.com/jurasikasan/units/task_board/internal/task"

	httpHelper "github.com/jurasikasan/units/shared/http"
	redisHelper "github.com/jurasikasan/units/shared/redis"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// @title          Task Board
// @version        1.0
// @description    The task board: projects, tasks with a per-project spoken
// @description    handle (ABC-12), the DRAFT→TODO→IN_PROGRESS→QA→UAT→DONE flow
// @description    plus CANCELLED, assignees and reporters by nick, dependencies,
// @description    subtasks, labels and components. Every status move and
// @description    assignee write is published to the board_events Redis stream.
// @description
// @description    The board also serves an MCP endpoint on /mcp, which is not
// @description    described here.
// @host           localhost:8099
// @schemes        http
func main() {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8099"
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

	// The board publishes what moved (task.EventsStream). GetRedisClient
	// waits for Redis and exits if it never comes, same as the Postgres ping
	// above: a board that silently publishes nothing is the failure that is
	// hard to see from outside.
	rdb := redisHelper.GetRedisClient(ctx)
	defer func() { _ = rdb.Close() }()

	svc := task.NewService(task.NewRepository(pool), task.NewRedisPublisher(rdb))

	r := newRouter(task.NewHandler(svc), pool)

	// /mcp sits beside the REST router rather than in it: it is one endpoint
	// speaking a protocol of its own (GET, POST and DELETE on the same path,
	// session ids in headers), not a route with a swagger entry, and keeping it
	// out of newRouter keeps routes_documented_test.go about the REST surface.
	board := task.NewMCPServer(svc)
	top := http.NewServeMux()
	top.Handle("/mcp", httpHelper.LoggingMiddleware(
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return board }, nil)))
	top.Handle("/", r)

	log.Printf("task_board listening on :%s (MCP on /mcp)", port)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           top,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// newRouter is the whole routing table: what RegisterRoutes decides plus the
// probes, in one place, so that routes_documented_test.go checks the router the
// server actually serves rather than a copy of its registration.
func newRouter(h *task.Handler, db pinger) *mux.Router {
	r := mux.NewRouter()
	r.Use(httpHelper.LoggingMiddleware)
	task.RegisterRoutes(r, h)
	r.HandleFunc("/health", healthHandler).Methods(http.MethodGet)
	r.HandleFunc("/ready", readyHandler(db)).Methods(http.MethodGet)
	return r
}

// version is the tree this binary was built from, stamped at build time by the
// Dockerfile: -ldflags "-X main.version=$VERSION", where VERSION comes from
// `git describe --tags --match 'v*' --always --dirty`.
// "dev" means nobody stamped it — a plain go build, or an image built without
// the arg. Never empty: an empty field reads the same as a missing one.
var version = "dev"

// healthHandler godoc
// @Summary  Liveness probe and build version
// @Description  Answers if the process is serving HTTP at all, and says nothing
// @Description  about Postgres. version is the git describe of the tree the
// @Description  binary was built from, so a deployment can be compared with
// @Description  its checkout to find services running stale code.
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

// readyTimeout bounds the probe's ping. It must stay under the compose
// healthcheck timeout, so a hung database is reported as 503 by this handler
// rather than as a killed probe.
const readyTimeout = 2 * time.Second

// readyHandler godoc
// @Summary  Readiness probe: can this instance reach Postgres
// @Description  200 when a ping to Postgres succeeds, 503 when it does not.
// @Description  This is what a compose healthcheck should ask, so a stopped
// @Description  database turns the container unhealthy. Kept apart from
// @Description  /health on purpose: /health must keep answering with the
// @Description  version while the database is down, because a reader of the
// @Description  version treats any non-200 as unreachable.
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
