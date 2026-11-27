package brain

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
)

// DefaultPrompt is the system prompt when BRAIN_PROMPT_FILE names none.
//
//go:embed prompt.txt
var DefaultPrompt string

// Config is the mind's: the gate, the buffers, the tick, recall, who is who.
type Config struct {
	// Closed channels: their events are dropped before the buffer.
	Closed map[string]bool
	// Keep is each channel's lifetime in perception time; KeepDefault any other's.
	Keep        map[string]time.Duration
	KeepDefault time.Duration
	// BufferMax bounds each channel's buffer, the oldest pushed out.
	BufferMax int
	// Settle: a tick fires this long after the last event, with a
	// recognition buffered and no start-of-input open.
	Settle time.Duration
	// MaxWait: a tick fires once the oldest buffered percept has waited this long.
	MaxWait time.Duration
	// Bind: percepts of different channels this close in perception time are one moment.
	Bind time.Duration
	// Recall is how many entries of the thread are read back as context.
	Recall int
	// Prompt is the system prompt.
	Prompt string
	// Self is the brain's subject id; Senders the senses', by channel.
	Self    uuid.UUID
	Senders map[string]uuid.UUID
	// ModelTimeout bounds one thought.
	ModelTimeout time.Duration
	// ActTimeout bounds one act on the body.
	ActTimeout time.Duration
}

// SubjectFor gives the subject id a channel writes into memory as: the
// configured one, or one derived from the channel's name, the same on every
// start — a sense that comes by config needs no id to be written down.
func (c Config) SubjectFor(channel string) uuid.UUID {
	if id, ok := c.Senders[channel]; ok {
		return id
	}
	return DerivedSubject("channel:" + channel)
}

// DerivedSubject is a stable subject id for a name, when none is configured.
func DerivedSubject(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("urn:units:brain:"+name))
}

// Mind is the loop: events in, gate, buffers, tick, one thought at a time.
type Mind struct {
	Cfg    Config
	Model  Model
	Store  Store
	Memory *Memory
	// Body is what the brain acts through; nil: it only thinks.
	Body   *Body
	Counts *Counts
	Log    *slog.Logger
	// Now is the brain's clock; time.Now when nil.
	Now func() time.Time

	buffered atomic.Int64
	bufs     *Buffers
	opens    map[string]time.Time // channel + request_id of a start-of-input → when it came
	seen     *recent
	// Since the last tick: a recognition came; when the last event came;
	// when the oldest buffered percept came.
	recognized bool
	lastEvent  time.Time
	waiting    time.Time
}

// Buffered is how many percepts wait for a tick now.
func (m *Mind) Buffered() int { return int(m.buffered.Load()) }

func (m *Mind) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Run takes events until ctx ends or events is closed, and thinks.
func (m *Mind) Run(ctx context.Context, events <-chan Event) {
	m.bufs = &Buffers{Keep: m.Cfg.Keep, KeepDefault: m.Cfg.KeepDefault, Max: m.Cfg.BufferMax, Counts: m.Counts}
	m.opens = map[string]time.Time{}
	m.seen = newRecent(4096)

	poll := m.Cfg.Settle / 5
	if poll < 5*time.Millisecond {
		poll = 5 * time.Millisecond
	}
	if poll > 100*time.Millisecond {
		poll = 100 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	done := make(chan struct{}, 1)
	thinking := false

	for {
		select {
		case <-ctx.Done():
			if thinking {
				<-done
			}
			return
		case ev, ok := <-events:
			if !ok {
				events = nil // drain what is buffered, then stop when ctx ends
				continue
			}
			m.take(ev)
		case <-done:
			thinking = false
		case <-ticker.C:
		}
		if !thinking && m.due() {
			ps := m.bufs.Take()
			m.buffered.Store(0)
			m.recognized, m.waiting = false, time.Time{}
			if len(ps) == 0 {
				continue
			}
			thinking = true
			go func() {
				m.think(ctx, ps)
				done <- struct{}{}
			}()
		}
	}
}

// take passes one event through the gate into the buffers.
func (m *Mind) take(ev Event) {
	if ev.Kind == Ignored {
		m.Counts.Ignored.Add(1)
		return
	}
	if !m.seen.add(ev.Key()) {
		m.Counts.Duplicates.Add(1)
		return
	}
	if m.Cfg.Closed[ev.Channel] {
		m.Counts.Gated.Add(1)
		return
	}
	now := m.now()
	m.lastEvent = now
	key := ev.Channel + "\x00" + ev.RequestID
	if ev.Kind == Start {
		m.opens[key] = now
		return
	}
	delete(m.opens, key)
	p := *ev.Percept
	m.Log.Info("percept", "channel", p.Channel, "type", p.Type, "request_id", p.RequestID,
		"started_at", p.Started, "ended_at", p.Ended, "ce_time", p.Sent, "received", p.Received,
		"lag_ms", p.Received.Sub(p.Ended).Milliseconds(), "frame", p.Frame != nil, "empty", p.Empty())
	if p.Empty() {
		m.Counts.Empty.Add(1)
		return
	}
	if m.bufs.Len() == 0 {
		m.waiting = now
	}
	m.recognized = true
	m.bufs.Add(p)
	m.buffered.Store(int64(m.bufs.Len()))
}

// due: something is buffered, and either the channels have settled after a
// recognition, or the oldest percept has waited BRAIN_MAX_WAIT.
func (m *Mind) due() bool {
	now := m.now()
	for k, at := range m.opens { // a start whose recognition never came holds nothing for ever
		if now.Sub(at) > m.Cfg.MaxWait {
			delete(m.opens, k)
		}
	}
	if m.bufs.Len() == 0 {
		return false
	}
	if now.Sub(m.waiting) >= m.Cfg.MaxWait {
		return true
	}
	return m.recognized && len(m.opens) == 0 && now.Sub(m.lastEvent) >= m.Cfg.Settle
}

// think makes one thought of ps: recall, the model, the log, memory.
func (m *Mind) think(ctx context.Context, ps []Percept) {
	moments := Bind(ps, m.Cfg.Bind)
	start, end := Span(ps)

	msgs := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(m.Cfg.Prompt)}
	recall, err := m.Memory.Recall(ctx, m.Cfg.Recall)
	if err != nil {
		m.Counts.MemoryErrs.Add(1)
		m.Log.Warn("memory: recall failed, thinking without it", "err", err.Error())
	}
	msgs = append(msgs, m.recallMessages(recall)...)
	images := 0
	for _, moment := range moments {
		var parts []openai.ChatCompletionContentPartUnionParam
		for _, p := range moment {
			parts = append(parts, openai.TextContentPart(Line(p)))
			if p.Frame == nil {
				continue
			}
			raw, err := m.Store.Fetch(ctx, p.Frame.SHA256)
			if err != nil {
				m.Counts.FrameErrors.Add(1)
				m.Log.Warn("frame: not fetched, text only", "channel", p.Channel, "request_id", p.RequestID, "err", err.Error())
				parts = append(parts, openai.TextContentPart("(the frame of this could not be fetched)"))
				continue
			}
			images++
			parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: "data:" + p.Frame.ContentType + ";base64," + base64.StdEncoding.EncodeToString(raw)}))
		}
		msgs = append(msgs, openai.UserMessage(parts))
	}

	tools := m.tools(ctx)
	tctx, cancel := context.WithTimeout(ctx, m.Cfg.ModelTimeout)
	asked := m.now()
	answer, err := m.Model.Think(tctx, msgs, tools)
	cancel()
	answered := m.now()
	sentFirst, sentLast := sentSpan(ps)
	attrs := []any{
		"span_start", start, "span_end", end,
		"lag_ms", answered.Sub(end).Milliseconds(), "model_ms", answered.Sub(asked).Milliseconds(),
		"percepts", len(ps), "moments", len(moments), "images", images, "channels", channels(ps),
		"ce_time_first", sentFirst, "ce_time_last", sentLast, "tools", len(tools), "calls", len(answer.Calls),
	}
	var act *Act
	if err != nil {
		m.Counts.ModelErrors.Add(1)
		m.Log.Warn("no thought: the model did not answer", append(attrs, "err", err.Error())...)
	} else {
		m.Counts.Thoughts.Add(1)
		m.Log.Info("thought", append([]any{"thought", answer.Text}, attrs...)...)
		act = m.actOn(ctx, answer, len(tools) > 0)
	}

	m.remember(ctx, ps, answer.Text, act, start, end)
}

// tools lists the body's tools for this thought: listed each time, so a
// body whose list changed is seen as it is now. A body that does not answer
// leaves the thought without acts.
func (m *Mind) tools(ctx context.Context) []openai.ChatCompletionToolUnionParam {
	if m.Body == nil {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tools, err := m.Body.Tools(lctx)
	if err != nil {
		m.Counts.BodyErrors.Add(1)
		m.Log.Warn("body: tools not listed, thinking without acting", "err", err.Error())
		return nil
	}
	return tools
}

// Act is one act on the body: the call the model made, and what came back.
type Act struct {
	ID, Name       string
	Args           json.RawMessage // a JSON object
	Result         string
	IsError        bool
	Started, Ended time.Time
}

// actOn takes the model's answer to one act: its first tool call is done on
// the body; any further one is ignored and counted. No call is a skip. What
// the body answers is logged and remembered, never sent back to the model
// within this thought: the next turn sees what came of it through the
// senses, and through memory.
func (m *Mind) actOn(ctx context.Context, a Answer, offered bool) *Act {
	if !offered { // no tools were given: a call is the model's own invention, not an act
		m.Counts.IgnoredCalls.Add(int64(len(a.Calls)))
		return nil
	}
	if len(a.Calls) == 0 {
		m.Counts.Skips.Add(1)
		m.Log.Info("act: none, the model called no tool")
		return nil
	}
	if extra := len(a.Calls) - 1; extra > 0 {
		m.Counts.IgnoredCalls.Add(int64(extra))
	}
	call := a.Calls[0]
	act := &Act{ID: call.ID, Name: call.Function.Name, Args: json.RawMessage("{}"), Started: m.now()}
	if act.ID == "" {
		act.ID = "call_" + uuid.NewString()
	}
	var args map[string]any
	raw := strings.TrimSpace(call.Function.Arguments)
	if raw == "" {
		raw = "{}"
	}
	switch {
	case json.Unmarshal([]byte(raw), &args) != nil || args == nil:
		act.Result, act.IsError = "bad_arguments: not a JSON object: "+truncate(raw, 200), true
	default:
		act.Args = json.RawMessage(raw)
		timeout := m.Cfg.ActTimeout
		if timeout <= 0 {
			timeout = DefaultActTimeout
		}
		actx, cancel := context.WithTimeout(ctx, timeout)
		result, isError, err := m.Body.Act(actx, act.Name, args)
		cancel()
		if err != nil {
			result, isError = err.Error(), true
		}
		act.Result, act.IsError = result, isError
	}
	act.Ended = m.now()
	m.Counts.Acts.Add(1)
	if act.IsError {
		m.Counts.ActErrors.Add(1)
	}
	m.Log.Info("act", "tool", act.Name, "call_id", act.ID, "args", string(act.Args), "result", act.Result,
		"is_error", act.IsError, "act_ms", act.Ended.Sub(act.Started).Milliseconds(), "ignored_calls", len(a.Calls)-1)
	return act
}

// remember appends each percept, then the thought with its act's call, then
// what the act answered, each with occurred.
func (m *Mind) remember(ctx context.Context, ps []Percept, thought string, act *Act, start, end time.Time) {
	id, opened, err := m.Memory.Resolve(ctx)
	if err != nil {
		m.Counts.MemoryErrs.Add(1)
		m.Log.Warn("memory: no thread, nothing written", "err", err.Error())
		return
	}
	if opened {
		m.Log.Info("memory: thread opened", "thread", id, "self", m.Cfg.Self.String())
	}
	var entries []Entry
	for _, p := range ps {
		content := []Block{{Type: "text", Text: Line(p)}}
		if p.Frame != nil {
			content = append(content, Block{Type: "image", URL: m.Store.RefURL(p.Frame.SHA256),
				MediaType: p.Frame.ContentType, SHA256: p.Frame.SHA256})
		}
		pend := p.Ended
		entries = append(entries, Entry{Sender: m.Cfg.SubjectFor(p.Channel).String(), Kind: "create",
			Content: content, Occurred: &Occurred{Start: p.Started, End: &pend}})
	}
	var mine []Block
	if thought != "" {
		mine = append(mine, Block{Type: "text", Text: thought})
	}
	if act != nil {
		mine = append(mine, Block{Type: "tool_call", ID: act.ID, Name: act.Name, Args: act.Args})
	}
	if len(mine) > 0 {
		entries = append(entries, Entry{Sender: m.Cfg.Self.String(), Kind: "create",
			Content: mine, Occurred: &Occurred{Start: start, End: &end}})
	}
	if act != nil {
		ended := act.Ended
		entries = append(entries, Entry{Sender: m.Cfg.Self.String(), Kind: "create",
			Content:  []Block{{Type: "tool_result", CallID: act.ID, Text: act.Result, IsError: act.IsError}},
			Occurred: &Occurred{Start: act.Started, End: &ended}})
	}
	for _, e := range entries {
		if err := m.Memory.Append(ctx, e); err != nil {
			m.Counts.MemoryErrs.Add(1)
			m.Log.Warn("memory: append failed, the rest not written", "err", err.Error())
			return
		}
	}
}

// recallMessages: the brain's own entries are what it thought and did
// (assistant), every other entry what it perceived (user). Only words come
// back: a recalled frame is its line of words, not its picture.
//
// An act is recalled as OpenAI has it: the assistant's message with its
// tool call, then the tool's message with what the body answered — when the
// answer is the next entry, as the brain writes it. A call whose answer was
// not recalled (cut by the recall window, or never written) is said in
// words instead, and so is an answer whose call was not: a tool message
// without its call, or a call without its answer, is refused by the API.
func (m *Mind) recallMessages(es []Entry) []openai.ChatCompletionMessageParamUnion {
	self := m.Cfg.Self.String()
	var out []openai.ChatCompletionMessageParamUnion
	for i := 0; i < len(es); i++ {
		e := es[i]
		if e.Kind != "create" {
			continue
		}
		var texts []string
		var call *Block
		for k, b := range e.Content {
			switch {
			case b.Type == "text" && b.Text != "":
				texts = append(texts, b.Text)
			case b.Type == "tool_call" && call == nil && e.Sender == self:
				call = &e.Content[k]
			case b.Type == "tool_call":
				texts = append(texts, "(did "+b.Name+" "+argsText(b.Args)+")")
			case b.Type == "tool_result":
				texts = append(texts, "(what came of an act: "+b.Text+")")
			}
		}
		text := strings.Join(texts, "\n")
		if call != nil {
			if res := resultFor(es, i+1, call.ID, self); res != nil {
				msg := openai.ChatCompletionAssistantMessageParam{ToolCalls: []openai.ChatCompletionMessageToolCallUnionParam{{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{ID: call.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: call.Name, Arguments: argsText(call.Args)}}}}}
				if text != "" {
					msg.Content.OfString = openai.String(text)
				}
				out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &msg}, openai.ToolMessage(res.Text, call.ID))
				i++
				continue
			}
			text = strings.TrimSpace(text + "\n(did " + call.Name + " " + argsText(call.Args) + "; what came of it is not remembered)")
		}
		if text == "" {
			continue
		}
		if e.Sender == self {
			out = append(out, openai.AssistantMessage(text))
		} else {
			out = append(out, openai.UserMessage(text))
		}
	}
	return out
}

// resultFor is the tool_result answering callID when the entry at i is the
// brain's and holds one.
func resultFor(es []Entry, i int, callID, self string) *Block {
	if i >= len(es) || es[i].Sender != self || es[i].Kind != "create" {
		return nil
	}
	for k, b := range es[i].Content {
		if b.Type == "tool_result" && b.CallID == callID {
			return &es[i].Content[k]
		}
	}
	return nil
}

func argsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// Line is a percept in words for the model and for memory:
// [21:00:12.500 sight] a person in the corridor — perception time in UTC,
// the channel, what was perceived. Words that came as speech (MRCP's
// input_type) are quoted, as said; text read in a scene follows as read: "…".
func Line(p Percept) string {
	var what []string
	if p.Text != "" {
		if p.InputType == "speech" {
			what = append(what, `"`+p.Text+`"`)
		} else {
			what = append(what, p.Text)
		}
	}
	if p.Read != "" {
		what = append(what, `read: "`+p.Read+`"`)
	}
	if len(what) == 0 {
		w := "(no words"
		if p.Cause != "" && p.Cause != "success" {
			w += ": " + p.Cause
		}
		what = append(what, w+")")
	}
	return "[" + p.Started.UTC().Format("15:04:05.000") + " " + p.Channel + "] " + strings.Join(what, "; ")
}

func channels(ps []Percept) []string {
	set := map[string]bool{}
	for _, p := range ps {
		set[p.Channel] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func sentSpan(ps []Percept) (first, last time.Time) {
	for _, p := range ps {
		if p.Sent.IsZero() {
			continue
		}
		if first.IsZero() || p.Sent.Before(first) {
			first = p.Sent
		}
		if p.Sent.After(last) {
			last = p.Sent
		}
	}
	return first, last
}

// recent remembers the last n keys, to drop an event delivered twice.
type recent struct {
	keys []string
	set  map[string]bool
	next int
}

func newRecent(n int) *recent { return &recent{keys: make([]string, n), set: map[string]bool{}} }

// add is false when key was seen among the last n.
func (r *recent) add(key string) bool {
	if r.set[key] {
		return false
	}
	if old := r.keys[r.next]; old != "" {
		delete(r.set, old)
	}
	r.keys[r.next], r.set[key] = key, true
	r.next = (r.next + 1) % len(r.keys)
	return true
}
