package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/auth"
	"github.com/jurasikasan/units/auth_service/internal/db"
	"github.com/jurasikasan/units/auth_service/internal/ratelimit"
	"github.com/jurasikasan/units/auth_service/internal/session"
	"github.com/jurasikasan/units/auth_service/internal/token"

	redisHelper "github.com/jurasikasan/units/shared/redis"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	httpHelper "github.com/jurasikasan/units/shared/http"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("%s=%q is not a duration, using %s", key, v, fallback)
		return fallback
	}
	return d
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("%s=%q is not a number, using %d", key, v, fallback)
		return fallback
	}
	return n
}

// @title auth_service API
// @version 1.0
// @description Authentication front door: login, logout, session verification, "who am I", and password change.
// @BasePath /
func main() {
	ctx := context.Background()

	databaseURL := os.Getenv("AUTH_DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("AUTH_DATABASE_URL is required")
	}

	port := getEnv("AUTH_PORT", "8093")
	sessionTTL := getEnvDuration("AUTH_SESSION_TTL", 7*24*time.Hour)

	userRateLimit := getEnvInt("AUTH_LOGIN_RATE_LIMIT", 10)
	ipRateLimit := getEnvInt("AUTH_LOGIN_IP_RATE_LIMIT", 100)
	rateWindow := getEnvDuration("AUTH_LOGIN_RATE_WINDOW", 15*time.Minute)

	cfg := auth.Config{
		CookieName: getEnv("AUTH_COOKIE_NAME", auth.CookieName),
		CookieSecure: func() bool {
			v := getEnv("AUTH_COOKIE_SECURE", "true")
			b, err := strconv.ParseBool(v)
			if err != nil {
				log.Printf("WARNING: AUTH_COOKIE_SECURE=%q is not a boolean, using true (safe direction)", v)
				return true
			}
			return b
		}(),
	}
	if origins := os.Getenv("AUTH_ALLOWED_ORIGINS"); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
			}
		}
	}
	if !cfg.CookieSecure {

		log.Print("WARNING: AUTH_COOKIE_SECURE=false — session cookies will be sent over plain HTTP. Local development only.")
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("connect to auth database: %v", err)
	}
	defer pool.Close()

	if err := waitForDB(ctx, pool); err != nil {
		log.Fatalf("auth database never became ready: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("auth schema migration: %v", err)
	}

	rdb := redisHelper.GetRedisClient(ctx)
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Printf("closing redis: %v", err)
		}
	}()

	accounts := account.NewService(pool)
	sessions := session.NewStore(rdb, sessionTTL)

	tokens := token.NewStore(pool, rdb)
	svc := auth.NewService(accounts, sessions, tokens)
	userLimiter := ratelimit.NewLimiter(rdb, userRateLimit, rateWindow)
	ipLimiter := ratelimit.NewLimiter(rdb, ipRateLimit, rateWindow)
	handler := auth.NewHandler(svc, cfg, userLimiter, ipLimiter)

	seedFirstAccount(ctx, accounts)

	r := mux.NewRouter()

	r.HandleFunc("/login", handler.Login).Methods(http.MethodPost)
	r.HandleFunc("/logout", handler.Logout).Methods(http.MethodPost)
	r.HandleFunc("/verify", handler.Verify).Methods(http.MethodGet)

	r.HandleFunc("/me", handler.Me).Methods(http.MethodGet)

	r.HandleFunc("/password", handler.ChangePassword).Methods(http.MethodPost)
	r.HandleFunc("/health", handler.Health).Methods(http.MethodGet)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           httpHelper.LoggingMiddleware(r),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("auth_service listening on :%s (session ttl %s, cookie %q secure=%t)",
		port, sessionTTL, cfg.CookieName, cfg.CookieSecure)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server: %v", err)
	}
}

func waitForDB(ctx context.Context, pool *pgxpool.Pool) error {
	var lastErr error
	for i := 0; i < 30; i++ {
		if err := pool.Ping(ctx); err == nil {
			log.Print("connected to auth database")
			return nil
		} else {
			lastErr = err
		}
		log.Printf("waiting for auth database (retrying in 2s)")
		time.Sleep(2 * time.Second)
	}
	return lastErr
}

func seedFirstAccount(ctx context.Context, accounts *account.Service) {
	username := os.Getenv("AUTH_SEED_USERNAME")
	seedPassword := os.Getenv("AUTH_SEED_PASSWORD")
	if username == "" || seedPassword == "" {
		return
	}

	n, err := accounts.Count(ctx)
	if err != nil {
		log.Printf("seed skipped, cannot count accounts: %v", err)
		return
	}
	if n > 0 {
		return
	}

	a, err := accounts.Create(ctx, username, os.Getenv("AUTH_SEED_EMAIL"), seedPassword)
	if err != nil {
		log.Printf("seed account %q failed: %v", username, err)
		return
	}
	log.Printf("seeded first account %q (%s)", a.Username, a.ID)
}
