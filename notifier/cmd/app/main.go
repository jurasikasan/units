package main

import (
	"context"
	"fmt"
	"github.com/jurasikasan/units/notifier/internal/ops"
	"github.com/jurasikasan/units/notifier/internal/telegram"
	sharedOps "github.com/jurasikasan/units/shared/ops"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	sharedRedis "github.com/jurasikasan/units/shared/redis"

	"github.com/go-redis/redis/v8"
)

var version = "dev"

const (
	redisGroup = "notifier_group"

	consumerName = "notifier_1"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	token := os.Getenv("TELEGRAM_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	name := os.Getenv("NOTIFIER_NAME")

	if token == "" || chatID == "" {
		log.Fatal("TELEGRAM_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	if name == "" {
		log.Fatal("NOTIFIER_NAME is required (for example agent@home)")
	}

	cooldown, err := time.ParseDuration(getEnv("NOTIFIER_COOLDOWN", "15m"))
	if err != nil {
		log.Fatalf("NOTIFIER_COOLDOWN: %v", err)
	}
	minSeverity := sharedOps.Severity(getEnv("NOTIFIER_MIN_SEVERITY", string(sharedOps.SeverityError)))

	rdb := sharedRedis.GetRedisClient(ctx)
	tg := telegram.New(token, chatID, os.Getenv("TELEGRAM_API_BASE"))
	limiter := ops.NewLimiter(cooldown)

	log.Printf("notifier: name=%s stream=%s cooldown=%s min_severity=%s", name, sharedOps.Stream, cooldown, minSeverity)
	run(ctx, rdb, tg, limiter, cooldown, minSeverity, name, version)
}

type sender interface {
	Send(ctx context.Context, text string) error
}

func run(ctx context.Context, rdb *redis.Client, tg sender, limiter *ops.Limiter, cooldown time.Duration, minSeverity sharedOps.Severity, name, version string) {

	if err := tg.Send(ctx, fmt.Sprintf("%s 🟢 started (%s)", name, version)); err != nil {
		log.Printf("startup message: %v", err)
	}

	for {
		_, err := rdb.XGroupCreateMkStream(ctx, sharedOps.Stream, redisGroup, "0").Result()
		if err == nil || err.Error() == "BUSYGROUP Consumer Group name already exists" {
			break
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("waiting for stream %s (retrying in 2s): %v", sharedOps.Stream, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("notifier: context cancelled, stopping")
			return
		default:
		}

		streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    redisGroup,
			Consumer: consumerName,
			Streams:  []string{sharedOps.Stream, ">"},
			Count:    10,
			Block:    0,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("read %s: %v", sharedOps.Stream, err)
			time.Sleep(time.Second)
			continue
		}

		for _, s := range streams {
			for _, msg := range s.Messages {
				handle(ctx, rdb, tg, limiter, cooldown, minSeverity, name, msg)
			}
		}
	}
}

func handle(ctx context.Context, rdb *redis.Client, tg sender, limiter *ops.Limiter,
	cooldown time.Duration, minSeverity sharedOps.Severity, name string, msg redis.XMessage) {
	defer func() {
		if err := rdb.XAck(ctx, sharedOps.Stream, redisGroup, msg.ID).Err(); err != nil {
			log.Printf("ack %s: %v", msg.ID, err)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("recovered handling %s: %v", msg.ID, r)
		}
	}()

	e, err := sharedOps.FromValues(msg.Values)
	if err != nil {
		log.Printf("dropping %s: %v", msg.ID, err)
		return
	}
	if minSeverity == sharedOps.SeverityError && e.Severity != sharedOps.SeverityError {
		return
	}

	send, suppressed := limiter.Allow(e.DedupeKey())
	if !send {
		return
	}
	if err := tg.Send(ctx, ops.Format(name, e, suppressed, cooldown)); err != nil {
		log.Printf("send %s/%s: %v", e.Service, e.Stage, err)
	}
}
