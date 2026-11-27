// Package seat is the policy seat (CONTRACT.md): a person, or Claude, behind
// an OpenAI chat completions door. A request to the door is a turn, held
// until whoever sits in the seat its model names answers it, or its caller
// gives up; the answer goes back as a model's would.
package seat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Version is stamped at build time (-ldflags "-X github.com/jurasikasan/units/seat.Version=…").
var Version = "dev"

// Defaults of the knobs (unit.yaml config, CONTRACT §4).
const (
	DefaultPort = 8080
	DefaultHold = 10 * time.Minute
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// CheckName: a seat is named by the request's model, kept to lower-case
// letters, digits, '_' and '-'.
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q: a seat's name is lower-case letters, digits, '_' and '-', at most 63", name)
	}
	return nil
}

// Turn is one request held for the seat: exactly what the caller sent.
type Turn struct {
	ID      string          `json:"id"`
	Seat    string          `json:"seat"`
	At      time.Time       `json:"at"`
	Request json.RawMessage `json:"request"`

	answer chan Answer
}

// ToolCall is the one tool call an answer may make; Arguments a JSON object.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Answer is what whoever sits in the seat says to a turn: words, and at most
// one tool call.
type Answer struct {
	Text     string    `json:"text,omitempty"`
	ToolCall *ToolCall `json:"tool_call,omitempty"`
}

// Event is what a seat's watcher is told: a turn came, or one is gone
// (answered, withdrawn by its caller, or held too long).
type Event struct {
	Kind   string `json:"-"` // turn | gone
	Turn   *Turn  `json:"turn,omitempty"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// The ways an answer is refused.
var (
	ErrUnknown  = errors.New("unknown_turn")
	ErrAnswered = errors.New("answered")
	ErrBad      = errors.New("bad_answer")
)

// Counts are what GET /health reports.
type Counts struct {
	Held, Answered, Withdrawn, Expired, Refused atomic.Int64
}

// Snapshot is the counts as JSON.
func (c *Counts) Snapshot() map[string]int64 {
	return map[string]int64{"held": c.Held.Load(), "answered": c.Answered.Load(), "withdrawn": c.Withdrawn.Load(),
		"expired": c.Expired.Load(), "refused": c.Refused.Load()}
}

// Seats hold the turns of every seat. Nothing is kept across a restart: a
// turn lives as long as its caller waits.
type Seats struct {
	// Hold is the longest a turn is held, its caller waiting or not.
	Hold   time.Duration
	Counts Counts
	// Log, when set, tells each turn held and how it ended.
	Log *slog.Logger

	mu       sync.Mutex
	pending  map[string]*Turn // id → turn
	order    []string         // ids, oldest first
	answered map[string]bool  // ids answered lately: a second answer is refused
	done     []string         // answered ids, oldest first, at most 1024
	watchers map[string]map[chan Event]struct{}
}

// New makes an empty set of seats.
func New(hold time.Duration) *Seats {
	if hold <= 0 {
		hold = DefaultHold
	}
	return &Seats{Hold: hold, pending: map[string]*Turn{}, answered: map[string]bool{},
		watchers: map[string]map[chan Event]struct{}{}}
}

func (s *Seats) tell(seat string, e Event) {
	for ch := range s.watchers[seat] {
		select {
		case ch <- e:
		default: // a watcher that does not read is not waited for
		}
	}
}

// Ask holds a request as a turn of seat until it is answered (the answer),
// ctx ends (the caller gave up) or Hold passes.
func (s *Seats) Ask(ctx context.Context, seat string, request json.RawMessage) (Answer, *Turn, error) {
	t := &Turn{ID: "turn-" + uuid.NewString(), Seat: seat, At: time.Now().UTC(), Request: request, answer: make(chan Answer, 1)}
	s.mu.Lock()
	s.pending[t.ID] = t
	s.order = append(s.order, t.ID)
	s.tell(seat, Event{Kind: "turn", Turn: t})
	s.mu.Unlock()
	s.Counts.Held.Add(1)
	s.log("turn: held", "seat", seat, "turn", t.ID)

	timer := time.NewTimer(s.Hold)
	defer timer.Stop()
	select {
	case a := <-t.answer:
		s.log("turn: answered", "seat", seat, "turn", t.ID, "text", a.Text, "tool_call", a.ToolCall)
		return a, t, nil // forgotten by Answer
	case <-ctx.Done():
		s.forget(t, "withdrawn")
		s.Counts.Withdrawn.Add(1)
		s.log("turn: withdrawn by its caller", "seat", seat, "turn", t.ID)
		return Answer{}, t, ctx.Err()
	case <-timer.C:
		s.forget(t, "expired")
		s.Counts.Expired.Add(1)
		s.log("turn: held too long, let go", "seat", seat, "turn", t.ID)
		return Answer{}, t, fmt.Errorf("held %s with no answer", s.Hold)
	}
}

func (s *Seats) forget(t *Turn, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[t.ID]; !ok {
		return
	}
	delete(s.pending, t.ID)
	for i, id := range s.order {
		if id == t.ID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.tell(t.Seat, Event{Kind: "gone", ID: t.ID, Reason: reason})
}

// Answer gives a turn of seat its answer. The first answer wins; a second is
// ErrAnswered, a turn not held ErrUnknown, an answer the request does not
// allow ErrBad.
func (s *Seats) Answer(seat, id string, a Answer) error {
	s.mu.Lock()
	t := s.pending[id]
	switch {
	case t == nil && s.answered[id]:
		s.mu.Unlock()
		s.Counts.Refused.Add(1)
		return fmt.Errorf("%w: turn %s was answered already", ErrAnswered, id)
	case t == nil || t.Seat != seat:
		s.mu.Unlock()
		s.Counts.Refused.Add(1)
		return fmt.Errorf("%w: no turn %s held in seat %s", ErrUnknown, id, seat)
	}
	if err := Check(t.Request, &a); err != nil {
		s.mu.Unlock()
		s.Counts.Refused.Add(1)
		return err
	}
	s.answered[id] = true
	s.done = append(s.done, id)
	if len(s.done) > 1024 {
		delete(s.answered, s.done[0])
		s.done = s.done[1:]
	}
	s.mu.Unlock()
	s.forget(t, "answered")
	t.answer <- a
	s.Counts.Answered.Add(1)
	return nil
}

// Pending are the turns held for seat, oldest first.
func (s *Seats) Pending(seat string) []*Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Turn
	for _, id := range s.order {
		if t := s.pending[id]; t.Seat == seat {
			out = append(out, t)
		}
	}
	return out
}

// Watch tells what happens in seat from now on, first every turn held now;
// stop ends it.
func (s *Seats) Watch(seat string) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	s.mu.Lock()
	if s.watchers[seat] == nil {
		s.watchers[seat] = map[chan Event]struct{}{}
	}
	s.watchers[seat][ch] = struct{}{}
	for _, id := range s.order {
		if t := s.pending[id]; t.Seat == seat {
			select {
			case ch <- Event{Kind: "turn", Turn: t}:
			default:
			}
		}
	}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.watchers[seat], ch)
		if len(s.watchers[seat]) == 0 {
			delete(s.watchers, seat)
		}
		s.mu.Unlock()
	}
}

// Next is the oldest turn held for seat, waiting for one until ctx ends (nil).
func (s *Seats) Next(ctx context.Context, seat string) *Turn {
	ch, stop := s.Watch(seat)
	defer stop()
	for {
		if p := s.Pending(seat); len(p) > 0 {
			return p[0]
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
		}
	}
}

// request is what the seat reads of a chat request to check an answer.
type request struct {
	Model      string          `json:"model"`
	Stream     bool            `json:"stream"`
	ToolChoice json.RawMessage `json:"tool_choice"`
	Tools      []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func (r request) required() bool {
	var s string
	return json.Unmarshal(r.ToolChoice, &s) == nil && s == "required"
}

// Check holds an answer to its request: a tool call names a tool offered,
// with a JSON object of arguments; a request that requires a tool call gets
// one; an answer says something.
func Check(raw json.RawMessage, a *Answer) error {
	var r request
	_ = json.Unmarshal(raw, &r)
	a.Text = strings.TrimSpace(a.Text)
	if a.ToolCall == nil {
		if r.required() {
			return fmt.Errorf("%w: the request requires a tool call (tool_choice: required)", ErrBad)
		}
		if a.Text == "" {
			return fmt.Errorf("%w: an answer says something, or calls a tool", ErrBad)
		}
		return nil
	}
	offered := false
	for _, t := range r.Tools {
		if t.Function.Name == a.ToolCall.Name {
			offered = true
		}
	}
	if !offered {
		return fmt.Errorf("%w: %q is no tool the request offers", ErrBad, a.ToolCall.Name)
	}
	args := strings.TrimSpace(string(a.ToolCall.Arguments))
	if args == "" || args == "null" {
		args = "{}"
	}
	var obj map[string]any
	if json.Unmarshal([]byte(args), &obj) != nil || obj == nil {
		return fmt.Errorf("%w: arguments are not a JSON object", ErrBad)
	}
	a.ToolCall.Arguments = json.RawMessage(args)
	return nil
}

// Completion is an answer as OpenAI's chat completion: the message a model
// would have sent.
func Completion(t *Turn, a Answer) map[string]any {
	msg := map[string]any{"role": "assistant", "content": a.Text}
	finish := "stop"
	if a.ToolCall != nil {
		finish = "tool_calls"
		msg["tool_calls"] = []any{map[string]any{"id": "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24],
			"type": "function", "function": map[string]any{"name": a.ToolCall.Name, "arguments": string(a.ToolCall.Arguments)}}}
	}
	return map[string]any{"id": t.ID, "object": "chat.completion", "created": time.Now().Unix(), "model": t.Seat,
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": msg}}}
}

func (s *Seats) log(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Info(msg, args...)
	}
}
