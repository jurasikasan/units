// Package brain is the first brain: a minimal multiplexer of the senses into
// thoughts. It knows the senses only by their CloudEvents, the model only by
// OpenAI's chat completions, its memory only by threads' REST API (CONTRACT.md).
package brain

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// What the brain understands of an event's type: the MRCP recognizer shape
// every sense reports in, whatever its resource (speechrecog, sightrecog, …).
// A sense is known by the suffix, never by its resource.
const (
	suffixStart       = ".start-of-input"
	suffixRecognition = ".recognition-complete"
	// typeBodyAct is a body's record of an act done through it (body
	// CONTRACT §2.2): perceived when someone else did it, passed over when
	// it is the brain's own (known by its traceparent).
	typeBodyAct = "body.act-complete"
	// BodyChannel is the channel a body's acts are perceived on.
	BodyChannel = "body"
)

// Kind is what an event is to the brain.
type Kind int

const (
	// Ignored: a type the brain does not read; counted, then dropped.
	Ignored Kind = iota
	// Start: something is starting on a channel; holds the tick until its
	// recognition comes (or BRAIN_MAX_WAIT).
	Start
	// Recognition: something perceived, in words and maybe a frame.
	Recognition
	// BodyAct: an act done through the body, a percept of channel body
	// unless it is the brain's own.
	BodyAct
)

// Frame is the reference a sense gives for the raw input behind an event,
// the shape senses/sight's CONTRACT names for every sense. The bytes are
// fetched from artifact_store by SHA256.
type Frame struct {
	SHA256      string    `json:"sha256"`
	Space       string    `json:"space"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	At          time.Time `json:"at"`
}

// Percept is one recognition as the brain keeps it: perception time from
// the data, transport time from the envelope, arrival from the brain's clock.
type Percept struct {
	Channel   string
	Source    string // ce_source
	ID        string // ce_id
	Type      string // ce_type
	RequestID string
	Text      string
	Read      string
	InputType string
	Cause     string // completion_cause
	// Started and Ended are perception time: when it happened in the world.
	Started, Ended time.Time
	// Sent is transport time: ce_time, when the sense published it.
	Sent time.Time
	// Received is when the brain read it off the stream.
	Received   time.Time
	Frame      *Frame
	FrameError string
}

// Empty is a recognition with nothing in it to think about: no words, no frame.
func (p Percept) Empty() bool {
	return p.Text == "" && p.Read == "" && p.Frame == nil
}

// Event is one stream entry as the brain reads it.
type Event struct {
	Kind      Kind
	Type      string
	Channel   string
	Source    string
	ID        string
	RequestID string
	// Traceparent is the entry's ce_traceparent: whose act a BodyAct is.
	Traceparent string
	// Percept is set for a Recognition and a BodyAct.
	Percept *Percept
}

// Key identifies an event for deduplication: contracts/events.md, source + id.
func (e Event) Key() string { return e.Source + "\x00" + e.ID }

// ErrMalformed: an entry the brain cannot read as a sense's event.
var ErrMalformed = errors.New("malformed event")

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ChannelOf names a channel by the event's source: /senses/sight is sight.
func ChannelOf(source string) string {
	return path.Base(strings.TrimRight(source, "/"))
}

// ParseEvent reads a stream entry in the envelope of contracts/events.md.
// Only ce_source, ce_id, ce_type, ce_time, ce_traceparent and, for the types
// it reads, data's request_id, started_at, ended_at, text, read, input_type,
// completion_cause, frame and frame_error — and a body's act's tool, args,
// is_error, error, started_at, ended_at — are looked at.
func ParseEvent(fields map[string]any, received time.Time) (Event, error) {
	str := func(k string) string { s, _ := fields[k].(string); return s }
	e := Event{Type: str("ce_type"), Source: str("ce_source"), ID: str("ce_id"), Traceparent: str("ce_traceparent")}
	if e.Type == "" || e.Source == "" || e.ID == "" {
		return e, fmt.Errorf("%w: ce_type, ce_source and ce_id are required", ErrMalformed)
	}
	e.Channel = ChannelOf(e.Source)
	if e.Channel == "" || e.Channel == "." || e.Channel == "/" {
		return e, fmt.Errorf("%w: ce_source %q names no channel", ErrMalformed, e.Source)
	}
	switch {
	case e.Type == typeBodyAct:
		return parseBodyAct(e, str("data"), str("ce_time"), received)
	case strings.HasSuffix(e.Type, suffixStart):
		e.Kind = Start
	case strings.HasSuffix(e.Type, suffixRecognition):
		e.Kind = Recognition
	default:
		return e, nil
	}

	var data struct {
		RequestID  string          `json:"request_id"`
		Text       string          `json:"text"`
		Read       string          `json:"read"`
		InputType  string          `json:"input_type"`
		Cause      string          `json:"completion_cause"`
		StartedAt  time.Time       `json:"started_at"`
		EndedAt    time.Time       `json:"ended_at"`
		Frame      json.RawMessage `json:"frame"`
		FrameError string          `json:"frame_error"`
	}
	if err := json.Unmarshal([]byte(str("data")), &data); err != nil {
		return e, fmt.Errorf("%w: data: %v", ErrMalformed, err)
	}
	e.RequestID = data.RequestID
	if e.Kind == Start {
		return e, nil
	}
	if data.StartedAt.IsZero() {
		return e, fmt.Errorf("%w: a recognition without started_at", ErrMalformed)
	}
	p := &Percept{
		Channel: e.Channel, Source: e.Source, ID: e.ID, Type: e.Type,
		RequestID: data.RequestID, Text: strings.TrimSpace(data.Text), Read: strings.TrimSpace(data.Read),
		InputType: data.InputType, Cause: data.Cause,
		Started: data.StartedAt.UTC(), Ended: data.EndedAt.UTC(),
		Received: received, FrameError: data.FrameError,
	}
	if p.Ended.IsZero() || p.Ended.Before(p.Started) {
		p.Ended = p.Started
	}
	if t, err := time.Parse(time.RFC3339Nano, str("ce_time")); err == nil {
		p.Sent = t.UTC()
	}
	if len(data.Frame) > 0 && string(data.Frame) != "null" {
		var f Frame
		switch err := json.Unmarshal(data.Frame, &f); {
		case err != nil:
			p.FrameError = "frame: " + err.Error()
		case !sha256Pattern.MatchString(f.SHA256):
			p.FrameError = "frame: sha256 is not 64 lower-case hex digits"
		default:
			if f.ContentType == "" {
				f.ContentType = "image/jpeg"
			}
			p.Frame = &f
		}
	}
	e.Percept = p
	return e, nil
}

// parseBodyAct reads a body's act as a percept of channel body: "did <tool>
// <args>", when it was done.
func parseBodyAct(e Event, raw, ceTime string, received time.Time) (Event, error) {
	var data struct {
		Tool      string          `json:"tool"`
		Args      json.RawMessage `json:"args"`
		IsError   bool            `json:"is_error"`
		Error     string          `json:"error"`
		StartedAt time.Time       `json:"started_at"`
		EndedAt   time.Time       `json:"ended_at"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return e, fmt.Errorf("%w: data: %v", ErrMalformed, err)
	}
	if data.Tool == "" {
		return e, fmt.Errorf("%w: a body's act without its tool", ErrMalformed)
	}
	e.Kind, e.Channel = BodyAct, BodyChannel
	args := strings.TrimSpace(string(data.Args))
	if args == "" || args == "null" {
		args = "{}"
	}
	text := "did " + data.Tool + " " + args
	if data.IsError {
		text += " (failed: " + data.Error + ")"
	}
	p := &Percept{Channel: BodyChannel, Source: e.Source, ID: e.ID, Type: e.Type, Text: text,
		Started: data.StartedAt.UTC(), Ended: data.EndedAt.UTC(), Received: received}
	if t, err := time.Parse(time.RFC3339Nano, ceTime); err == nil {
		p.Sent = t.UTC()
	}
	if p.Started.IsZero() {
		p.Started = p.Sent
	}
	if p.Started.IsZero() {
		p.Started = received.UTC()
	}
	if p.Ended.IsZero() || p.Ended.Before(p.Started) {
		p.Ended = p.Started
	}
	e.Percept = p
	return e, nil
}
