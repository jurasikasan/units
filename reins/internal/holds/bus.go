package holds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	sharedevents "github.com/jurasikasan/units/shared/events"
)

// streamMax is about how many events reins_events keeps: each only wakes,
// and its reader reads the truth back.
const streamMax = 10000

// RedisBus publishes on reins_events in the envelope of
// contracts/events.md. Redis is not waited for: an event that cannot be
// put is logged, and the table stays the truth.
type RedisBus struct{ Client *redis.Client }

func (b RedisBus) Publish(ctx context.Context, typ, subject string, data map[string]any) error {
	f, err := sharedevents.Fields(Source, sharedevents.Event{Type: typ, Subject: subject, Data: data})
	if err != nil {
		return err
	}
	return b.Client.XAdd(ctx, &redis.XAddArgs{Stream: Stream, Values: f, MaxLen: streamMax, Approx: true}).Err()
}

// HTTPBrains asks a driver which profiles it has: its GET /profiles.
type HTTPBrains struct {
	base   string
	client *http.Client
}

func NewHTTPBrains(base string) *HTTPBrains {
	return &HTTPBrains{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (b *HTTPBrains) Profiles(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/profiles", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the driver's profiles: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("the driver's profiles: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var ps []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ps); err != nil {
		return nil, fmt.Errorf("the driver's profiles: %w", err)
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out, nil
}
