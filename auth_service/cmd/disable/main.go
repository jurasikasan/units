package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/auth"
	"github.com/jurasikasan/units/auth_service/internal/session"

	redisHelper "github.com/jurasikasan/units/shared/redis"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: disable <username>")
		os.Exit(2)
	}
	username := os.Args[1]

	ctx := context.Background()

	databaseURL := os.Getenv("AUTH_DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("AUTH_DATABASE_URL is required")
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("connect to auth database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("auth database not reachable: %v", err)
	}

	sessionTTL := 7 * 24 * time.Hour
	if v := os.Getenv("AUTH_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			sessionTTL = d
		} else {
			log.Printf("AUTH_SESSION_TTL=%q is not a duration, using %s", v, 7*24*time.Hour)
		}
	}

	rdb := redisHelper.GetRedisClient(ctx)
	defer func() {
		if cerr := rdb.Close(); cerr != nil {
			log.Printf("closing redis: %v", cerr)
		}
	}()

	accounts := account.NewService(pool)
	sessions := session.NewStore(rdb, sessionTTL)

	id, err := auth.Disable(ctx, accounts, sessions, username)
	if err != nil {
		if errors.Is(err, account.ErrAccountNotFound) {
			fmt.Fprintf(os.Stderr, "no such account: %s\n", username)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "disable %s: %v", username, err)
		if id != uuid.Nil {
			fmt.Fprintln(os.Stderr, "the disabled flag is already set; re-run to kill the sessions")
		}
		os.Exit(1)
	}

	fmt.Printf("disabled %s (%s), sessions removed\n", username, id)
}
