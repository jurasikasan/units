package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/jurasikasan/units/artifact_store/docs" // registers the generated OpenAPI spec
	"github.com/jurasikasan/units/artifact_store/internal/app"
	"github.com/jurasikasan/units/artifact_store/internal/store"
)

// @title       artifact_store API
// @version     1.0
// @description A store of named files in spaces: put, get, list and delete, and a read of the bytes by their sha256.
// @BasePath    /
func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8094"
	}

	// RETENTION is read at start (CONTRACT.md section 5): a malformed
	// value stops the start with a message that names it.
	retention, err := store.ParseRetention(os.Getenv("RETENTION"))
	if err != nil {
		log.Fatalf("artifact_store: %v", err)
	}

	// STORE_SWEEP_PERIOD is read at start (CONTRACT.md section 2): the
	// period of the expiry and the removal of unnamed bytes, a Go
	// duration (10m, 1h); without it the default of 1h. A malformed
	// value stops the start with a message that names it.
	sweepPeriod, err := store.ParseSweepPeriod(os.Getenv("STORE_SWEEP_PERIOD"))
	if err != nil {
		log.Fatalf("artifact_store: %v", err)
	}

	svc := store.NewService(context.Background(), retention, sweepPeriod)
	// In the background, once right after start and then every sweep
	// period: the expiry removes the files past their rule's time, and
	// the sweep takes the bytes no name points to any more (CONTRACT.md
	// section 5); a dependency that is down is a 503 on the routes, not
	// a failed start.
	svc.StartSweep(context.Background())

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           app.NewRouter(svc),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("artifact_store listening on :%s", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
