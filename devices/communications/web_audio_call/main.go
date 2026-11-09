package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	sredis "github.com/jurasikasan/units/shared/redis"
)

func configFromEnv() Config {
	cfg := Config{}
	if v := os.Getenv("WEBRTC_MAX_CALLS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxCalls = n
		}
	}
	if v := os.Getenv("WEBRTC_UNATTENDED_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.UnattendedTimeout = d
		}
	}
	cfg.PublicIP = os.Getenv("WEBRTC_PUBLIC_IP")
	if v := os.Getenv("WEBRTC_PORT_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.PortMin = n
		}
	}
	if v := os.Getenv("WEBRTC_PORT_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.PortMax = n
		}
	}
	cfg.AudioInHost = os.Getenv("WEBRTC_AUDIO_IN_HOST")
	if v := os.Getenv("WEBRTC_AUDIO_IN_PORT_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.AudioInPortMin = n
		}
	}
	if v := os.Getenv("WEBRTC_AUDIO_IN_PORT_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.AudioInPortMax = n
		}
	}
	return cfg
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rdb := sredis.GetRedisClient(ctx)

	line := NewLine(configFromEnv(), rdb, log)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           line.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("webrtc_line listening", "port", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
