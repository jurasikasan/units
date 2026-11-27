// Command body runs one body: its senses opened and served as its ears and
// eyes, its own perception republished onto its stream, its acts as MCP tools.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/jurasikasan/units/body"
	sharedredis "github.com/jurasikasan/units/shared/redis"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	hc := body.HostConfig{
		HearingURL:   envStr("HEARING_MCP_URL", ""),
		SightURL:     envStr("SIGHT_MCP_URL", ""),
		SpeakingURL:  envStr("SPEAKING_MCP_URL", ""),
		Group:        envStr("BODY_GROUP", body.DefaultHostGroup),
		SpeakTimeout: envDur("BODY_SPEAK_TIMEOUT", body.DefaultSpeakTimeout),
		Retry:        envDur("BODY_RETRY", body.DefaultRetry),
	}

	// A body named in the env is made at start, as a body run alone always
	// was: its group body_<name>, its MCP on /mcp too, GET /body. Without
	// one, the host carries only what is PUT.
	name := envStr("BODY_NAME", "")
	var spec body.Spec
	if name != "" {
		must(body.CheckName(name), "BODY_NAME")
		hc.Group = envStr("BODY_GROUP", body.GroupFor(name))
		spec = body.Spec{ID: envStr("BODY_ID", ""), Language: envStr("BODY_LANGUAGE", ""), Voice: envStr("BODY_VOICE", "")}
		if spec.ID != "" {
			_, err := uuid.Parse(spec.ID)
			must(err, "BODY_ID")
		}
		spec.Description = envStr("BODY_DESCRIPTION", "")
		if f := envStr("BODY_DESCRIPTION_FILE", ""); f != "" {
			if spec.Description != "" {
				log.Fatal("body: BODY_DESCRIPTION and BODY_DESCRIPTION_FILE are both set; one says what the body is")
			}
			raw, err := os.ReadFile(f)
			must(err, "BODY_DESCRIPTION_FILE")
			spec.Description = strings.TrimSpace(string(raw))
		}
		earshot, err := body.ParseEarshot(envStr("BODY_EARSHOT", ""))
		must(err, "BODY_EARSHOT")
		spec.Earshot = earshot
		if hc.HearingURL != "" {
			spec.Senses = append(spec.Senses, "hearing")
		}
		if hc.SightURL != "" {
			spec.Senses = append(spec.Senses, "sight")
		}
		if hc.SpeakingURL != "" && spec.Voice == "" {
			log.Fatal("body: SPEAKING_MCP_URL is set and BODY_VOICE is not: speaking has no default voice")
		}
		if hc.SpeakingURL == "" {
			spec.Voice = ""
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rdb := sharedredis.GetRedisClient(ctx)
	h := body.NewHost(hc, rdb, &http.Client{Timeout: 10 * time.Second}, logger)
	h.Default = name
	if stream := envStr("BODY_STREAM", ""); stream != "" {
		h.Overrides = func(c *body.Config) { c.Stream = stream }
	}
	logger.Info("body: up", "version", body.Version, "group", hc.Group, "hearing", hc.HearingURL != "",
		"sight", hc.SightURL != "", "speaking", hc.SpeakingURL != "", "default", name)

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	select {
	case <-h.Ready():
	case err := <-done:
		log.Fatalf("body: %v", err)
	}
	if name != "" {
		l, _, err := h.Put(name, spec)
		must(err, "the body of the env")
		logger.Info("body: the env's body made", "name", name, "id", l.ID, "earshot", spec.Earshot)
	}

	srv := &http.Server{Addr: ":" + strconv.Itoa(envInt("PORT", body.DefaultPort)), Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("body: %v", err)
		}
	}()
	if err := <-done; err != nil {
		log.Fatalf("body: %v", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func must(err error, name string) {
	if err != nil {
		log.Fatalf("body: %s: %v", name, err)
	}
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// envInt and envDur refuse a malformed value: a body started on a knob it
// did not read is worse than one that did not start.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Fatalf("body: %s=%q is not a positive number", name, v)
	}
	return n
}

func envDur(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Fatalf("body: %s=%q is not a positive duration", name, v)
	}
	return d
}
