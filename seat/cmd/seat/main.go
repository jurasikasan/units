// Command seat runs the policy seat: an OpenAI chat completions door whose
// answers come from a person or Claude sitting in the seat the model names.
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

	"github.com/jurasikasan/units/seat"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	hold := seat.DefaultHold
	if v := os.Getenv("SEAT_HOLD"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("seat: SEAT_HOLD=%q is not a positive duration", v)
		}
		hold = d
	}
	port := seat.DefaultPort
	if v := os.Getenv("PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Fatalf("seat: PORT=%q is not a port", v)
		}
		port = n
	}
	s := seat.New(hold)
	s.Log = logger
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: ":" + strconv.Itoa(port), Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logger.Info("seat: up", "version", seat.Version, "port", port, "hold", hold.String())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("seat: %v", err)
	}
}
