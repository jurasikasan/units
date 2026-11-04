package ops

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	sharedRedis "github.com/jurasikasan/units/shared/redis"

	"github.com/go-redis/redis/v8"
)

const Stream = sharedRedis.OpsEventsStream

type Severity string

const (
	SeverityError Severity = "error"

	SeverityWarn Severity = "warn"
)

type Event struct {
	Service string `json:"service"`

	Stage string `json:"stage"`

	Severity Severity `json:"severity"`

	Entity   string `json:"entity,omitempty"`
	EntityID string `json:"entity_id,omitempty"`

	Reason string `json:"reason"`
}

const MaxReasonLen = 200

func (e Event) DedupeKey() string {
	return strings.Join([]string{e.Service, e.Stage, e.Reason}, "|")
}

func (e *Event) Normalise() error {
	e.Service = strings.TrimSpace(e.Service)
	e.Stage = strings.TrimSpace(e.Stage)
	e.Reason = strings.TrimSpace(e.Reason)

	if e.Severity == "" {
		e.Severity = SeverityError
	}
	if e.Service == "" {
		return fmt.Errorf("ops event has no service")
	}
	if e.Stage == "" {
		return fmt.Errorf("ops event from %s has no stage", e.Service)
	}
	if e.Reason == "" {
		return fmt.Errorf("ops event from %s/%s has no reason", e.Service, e.Stage)
	}
	if strings.ContainsAny(e.Reason, "\r\n") {

		return fmt.Errorf("ops event from %s/%s reason contains a line break", e.Service, e.Stage)
	}
	if utf8.RuneCountInString(e.Reason) > MaxReasonLen {
		return fmt.Errorf("ops event from %s/%s reason is %d chars, more than the %d cap (payloads do not belong in this stream)",
			e.Service, e.Stage, utf8.RuneCountInString(e.Reason), MaxReasonLen)
	}
	if e.Severity != SeverityError && e.Severity != SeverityWarn {
		return fmt.Errorf("ops event from %s/%s has unknown severity %q", e.Service, e.Stage, e.Severity)
	}
	return nil
}

func (e Event) Values() map[string]any {
	v := map[string]any{
		"service":  e.Service,
		"stage":    e.Stage,
		"severity": string(e.Severity),
		"reason":   e.Reason,
	}
	if e.Entity != "" {
		v["entity"] = e.Entity
	}
	if e.EntityID != "" {
		v["entity_id"] = e.EntityID
	}
	return v
}

func FromValues(values map[string]any) (Event, error) {
	get := func(k string) string {
		s, _ := values[k].(string)
		return s
	}
	e := Event{
		Service:  get("service"),
		Stage:    get("stage"),
		Severity: Severity(get("severity")),
		Entity:   get("entity"),
		EntityID: get("entity_id"),
		Reason:   get("reason"),
	}
	return e, e.Normalise()
}

func Publish(ctx context.Context, rdb *redis.Client, e Event) error {
	if err := e.Normalise(); err != nil {
		return err
	}
	return rdb.XAdd(ctx, sharedRedis.XAddArgs(Stream, e.Values())).Err()
}
