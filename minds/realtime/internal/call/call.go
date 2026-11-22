// Package call holds one call in OpenAI's Realtime schema, made of organs:
// hearing finds the caller's utterances and their words, a model through an
// OpenAI-shaped chat endpoint answers, speaking says the answer and reports
// what of it went out. contracts/edges.md, "Realtime, event by event", is
// the map this follows.
package call

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/jurasikasan/units/minds/realtime/internal/audio"
	"github.com/jurasikasan/units/minds/realtime/internal/llm"
	"github.com/jurasikasan/units/minds/realtime/internal/organs"
)

// Organs is what every call shares: the organs, their event streams, the
// model's endpoint, the audio ports.
type Organs struct {
	Hearing, Speaking     *organs.Organ
	HearingEv, SpeakingEv *organs.Stream
	LLM                   *llm.Client
	Ports                 *audio.Ports
	// AudioHost is the name the organs reach this mind by, for speaking's
	// sound to come back to.
	AudioHost string
}

// Defaults are a session's settings before the client says otherwise.
type Defaults struct {
	Model        string // the chat model asked
	Voice        string // speaking's <engine>_<voice>
	Language     string // hearing's speech_language hint; "" lets STT guess
	Instructions string
}

// msPerByte24 is PCM16 at 24 kHz: 48 bytes a millisecond.
const bytesPerMs = audio.Rate * 2 / 1000

// deltaBytes is how much of the answer's sound goes in one
// response.output_audio.delta: 100 ms.
const deltaBytes = 100 * bytesPerMs

// item is a conversation item as the model will read it.
type item struct {
	id, role, text string
	pieces         []*piece // an answer's phrases, for truncate
}

// response is one answer being made: the model writing, speaking saying.
type response struct {
	id, item string
	cancel   context.CancelFunc
	phrases  phrases
	text     string // what was given to speaking, the transcript so far
	pieces   []*piece
	written  bool   // the model has finished writing
	failure  string // the model or speaking failed
	reason   string // cancelled: turn_detected or client_cancelled
	audioMs  int    // ms of its sound sent to the client
	pending  []byte // sound not yet sent
	// at is where each of speaking's packets begins in the answer's sound,
	// ms, by its RTP timestamp: a marker finds its sentence's start here.
	at       map[uint32]int
	truncate *int // the client's audio_end_ms, come before the answer was done
	next     bool // an utterance waits for an answer of its own
}

type (
	wsMessage struct{ raw []byte }
	closed    struct{ err error }
	// heardFirst: sound came before any session.update; time to take the call
	heardFirst struct{}
	wrote      struct {
		resp string
		text string
	}
	written struct {
		resp string
		err  error
	}
	sound struct {
		pcm []byte
		ts  uint32
	}
)

// Call is one call: the WebSocket, an actor owning everything below, and
// the goroutines that feed it.
type Call struct {
	o   *Organs
	ws  *websocket.Conn
	log *slog.Logger
	in  chan any // everything the actor handles, in order

	id, conv, model string
	instructions    string
	voice, language string
	llmModel        string
	// the ear and the turn timings asked for, given to hearing at open;
	// zero: hearing's own
	sttModel, sttPrompt string
	mergeMs             int
	prerollMs           *int
	createResponse      bool
	interrupt           bool

	hearing  string // hearing's session
	speaking string // speaking's session, opened for the first answer
	receiver *audio.Receiver

	// soundMu guards sender and early: sound that came before hearing was
	// open waits in early, in order, and goes first.
	soundMu sync.Mutex
	sender  *audio.Sender
	early   [][]byte
	asked   bool // heardFirst sent

	firstSound   time.Time // the first sound sent to hearing
	firstSoundMu sync.Mutex

	user    string // the utterance being heard: its item
	items   []*item
	resp    *response
	stopped bool
}

// Serve holds the call on ws until either side ends it. model is what the
// client asked for (?model=), shown in the session.
func Serve(ctx context.Context, o *Organs, d Defaults, ws *websocket.Conn, model string, log *slog.Logger) {
	c := &Call{
		o: o, ws: ws, in: make(chan any, 1024),
		id: "sess_" + short(), conv: "conv_" + short(), model: model,
		instructions: d.Instructions, voice: d.Voice, language: d.Language, llmModel: d.Model,
		createResponse: true, interrupt: true,
	}
	c.log = log.With("call", c.id)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.hangUp()
	c.send(ctx, map[string]any{"type": "session.created", "session": c.session()})
	// The ear opens once the client has said how it wants to be heard: at
	// its first session.update, or at its first sound if none came before.
	go c.read(ctx)
	c.run(ctx)
}

// takeOnce takes the call the first time it is asked; a failure ends it.
func (c *Call) takeOnce(ctx context.Context) {
	if c.hearing != "" || c.stopped {
		return
	}
	if err := c.take(ctx); err != nil {
		c.log.Error("call not taken", "err", err)
		c.send(ctx, errorEvent("server_error", "", err.Error()))
		_ = c.ws.Close(websocket.StatusInternalError, "the organs are not there")
		c.stopped = true
	}
}

// take answers the call. Here the mind takes every call that comes in; the
// model does not decide yet (one day it is told of the call and takes it,
// or not, by a tool, and hangs up when it wants). Taking it is opening an
// ear on it: hearing's session, which the caller's sound is sent to.
func (c *Call) take(ctx context.Context) error {
	args := map[string]any{}
	if c.language != "" {
		args["speech_language"] = c.language
	}
	if c.sttModel != "" {
		args["stt_model"] = c.sttModel
	}
	if c.sttPrompt != "" {
		args["stt_prompt"] = c.sttPrompt
	}
	if c.mergeMs > 0 {
		args["merge_window_ms"] = c.mergeMs
	}
	if c.prerollMs != nil {
		args["preroll_ms"] = *c.prerollMs
	}
	var opened struct {
		SessionID string `json:"session_id"`
		AudioIn   string `json:"audio_in"`
	}
	if err := c.o.Hearing.Call(ctx, "open", args, &opened); err != nil {
		return err
	}
	c.hearing = opened.SessionID
	ev := make(chan organs.Event, 256)
	c.o.HearingEv.Subscribe(c.hearing, ev)
	go forward(ctx, ev, c.in)
	s, err := audio.NewSender(opened.AudioIn)
	if err != nil {
		return fmt.Errorf("hearing's audio_in %q: %w", opened.AudioIn, err)
	}
	c.soundMu.Lock()
	c.sender = s
	for _, pcm := range c.early {
		if err := s.SendPCM24(pcm); err != nil {
			c.log.Warn("sound not sent to hearing", "err", err)
		}
	}
	c.early = nil
	c.soundMu.Unlock()
	c.log.Info("call taken", "model", c.model, "hearing", c.hearing, "audio_in", opened.AudioIn,
		"ear", c.sttModel, "merge_window_ms", c.mergeMs, "language", c.language)
	return nil
}

func forward(ctx context.Context, from <-chan organs.Event, to chan<- any) {
	for {
		select {
		case ev := <-from:
			select {
			case to <- ev:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// openMouth opens speaking's session, its sound sent to a port of ours.
func (c *Call) openMouth(ctx context.Context) error {
	r, err := c.o.Ports.Listen()
	if err != nil {
		return err
	}
	var opened struct {
		SessionID string `json:"session_id"`
	}
	if err := c.o.Speaking.Call(ctx, "open", map[string]any{"to": r.Address(c.o.AudioHost), "voice_name": c.voice}, &opened); err != nil {
		r.Close()
		return err
	}
	c.receiver, c.speaking = r, opened.SessionID
	ev := make(chan organs.Event, 256)
	c.o.SpeakingEv.Subscribe(c.speaking, ev)
	go forward(ctx, ev, c.in)
	go r.Run(func(pcm []byte, ts uint32) {
		select {
		case c.in <- sound{pcm, ts}:
		case <-ctx.Done():
		}
	})
	c.log.Info("mouth opened", "speaking", c.speaking, "voice", c.voice)
	return nil
}

// hangUp closes what the call opened.
func (c *Call) hangUp() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c.resp != nil && c.resp.cancel != nil {
		c.resp.cancel()
	}
	if c.hearing != "" {
		c.o.HearingEv.Unsubscribe(c.hearing)
		if err := c.o.Hearing.Call(ctx, "close", map[string]any{"session_id": c.hearing}, nil); err != nil {
			c.log.Warn("hearing not closed", "err", err)
		}
	}
	if c.speaking != "" {
		c.o.SpeakingEv.Unsubscribe(c.speaking)
		if err := c.o.Speaking.Call(ctx, "close", map[string]any{"session_id": c.speaking}, nil); err != nil {
			c.log.Warn("speaking not closed", "err", err)
		}
	}
	c.soundMu.Lock()
	if c.sender != nil {
		_ = c.sender.Close()
	}
	c.soundMu.Unlock()
	if c.receiver != nil {
		c.receiver.Close()
	}
}

// read takes the client's events. Sound goes straight on to hearing, as it
// comes; everything else to the actor, in order.
func (c *Call) read(ctx context.Context) {
	appended := 0
	defer func() { c.log.Info("sound in", "chunks", appended) }()
	for {
		_, raw, err := c.ws.Read(ctx)
		if err != nil {
			select {
			case c.in <- closed{err}:
			case <-ctx.Done():
			}
			return
		}
		var head struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if json.Unmarshal(raw, &head) == nil && head.Type == "input_audio_buffer.append" {
			pcm, err := base64.StdEncoding.DecodeString(head.Audio)
			if err != nil {
				continue
			}
			c.firstSoundMu.Lock()
			if c.firstSound.IsZero() {
				c.firstSound = time.Now()
			}
			c.firstSoundMu.Unlock()
			c.hear(ctx, pcm)
			appended++
			continue
		}
		select {
		case c.in <- wsMessage{raw}:
		case <-ctx.Done():
			return
		}
	}
}

// hear sends the caller's sound to hearing, or keeps it until hearing is
// open (and asks for the call to be taken).
func (c *Call) hear(ctx context.Context, pcm []byte) {
	c.soundMu.Lock()
	if c.sender != nil {
		err := c.sender.SendPCM24(pcm)
		c.soundMu.Unlock()
		if err != nil {
			c.log.Warn("sound not sent to hearing", "err", err)
		}
		return
	}
	c.early = append(c.early, pcm)
	ask := !c.asked
	c.asked = true
	c.soundMu.Unlock()
	if ask {
		select {
		case c.in <- heardFirst{}:
		case <-ctx.Done():
		}
	}
}

// run is the actor: it alone touches the call's state.
func (c *Call) run(ctx context.Context) {
	for {
		var m any
		select {
		case m = <-c.in:
		case <-ctx.Done():
			return
		}
		switch m := m.(type) {
		case closed:
			var ce websocket.CloseError
			if errors.As(m.err, &ce) {
				c.log.Info("call closed", "by", "client", "code", int(ce.Code))
			} else {
				c.log.Info("call closed", "err", m.err)
			}
			return
		case heardFirst:
			c.takeOnce(ctx)
		case wsMessage:
			c.client(ctx, m.raw)
		case organs.Event:
			c.organ(ctx, m)
		case wrote:
			c.wrote(ctx, m)
		case written:
			c.written(ctx, m)
		case sound:
			c.sound(ctx, m.pcm, m.ts)
		}
		if c.stopped {
			return
		}
	}
}

// --- the client's events

func (c *Call) client(ctx context.Context, raw []byte) {
	var ev struct {
		Type    string          `json:"type"`
		EventID string          `json:"event_id"`
		Session json.RawMessage `json:"session"`
		Item    json.RawMessage `json:"item"`
		ItemID  string          `json:"item_id"`
		Content int             `json:"content_index"`
		EndMs   *int            `json:"audio_end_ms"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.send(ctx, errorEvent("invalid_request_error", "", "not JSON: "+err.Error()))
		return
	}
	c.log.Info("event", "dir", "in", "type", ev.Type, "bytes", len(raw))
	switch ev.Type {
	case "session.update":
		c.update(ev.Session)
		c.takeOnce(ctx)
		if !c.stopped {
			c.send(ctx, map[string]any{"type": "session.updated", "session": c.session()})
		}
	case "conversation.item.create":
		c.create(ctx, ev.Item, ev.EventID)
	case "conversation.item.truncate":
		end := 0
		if ev.EndMs != nil {
			end = *ev.EndMs
		}
		c.truncate(ctx, ev.ItemID, ev.Content, end, ev.EventID)
	case "conversation.item.delete":
		c.delete(ctx, ev.ItemID, ev.EventID)
	case "conversation.item.retrieve":
		if it := c.find(ev.ItemID); it != nil {
			c.send(ctx, map[string]any{"type": "conversation.item.retrieved", "item": it.wire("completed")})
		} else {
			c.send(ctx, errorEvent("invalid_request_error", ev.EventID, "no item "+ev.ItemID))
		}
	case "response.create":
		if c.resp != nil {
			c.send(ctx, errorEvent("invalid_request_error", ev.EventID, "Conversation already has an active response"))
			return
		}
		c.answer(ctx)
	case "response.cancel":
		if c.resp != nil {
			c.cancel(ctx, "client_cancelled")
		}
	case "input_audio_buffer.commit", "input_audio_buffer.clear":
		// hearing finds the utterances itself; turns by the client
		// (push-to-talk) are not carried yet.
		c.send(ctx, errorEvent("invalid_request_error", ev.EventID, ev.Type+" is not carried: utterances are found by hearing"))
	default:
		c.send(ctx, errorEvent("invalid_request_error", ev.EventID, "not carried: "+ev.Type))
	}
}

// update takes what of session.update this call can follow: instructions,
// the voice (until the first answer has been said, as in the schema), and
// turn detection's create_response and interrupt_response; null turn
// detection means the client answers and interrupts itself.
func (c *Call) update(raw json.RawMessage) {
	var s struct {
		Instructions *string `json:"instructions"`
		// ours: the chat model that answers, which the schema has no field
		// for (its model is the realtime model itself)
		ChatModel string `json:"chat_model"`
		Audio     struct {
			Input struct {
				Transcription *struct {
					Model    string `json:"model"`
					Language string `json:"language"`
					Prompt   string `json:"prompt"`
				} `json:"transcription"`
				TurnDetection json.RawMessage `json:"turn_detection"`
			} `json:"input"`
			Output struct {
				Voice string `json:"voice"`
			} `json:"output"`
		} `json:"audio"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return
	}
	if s.Instructions != nil {
		c.instructions = *s.Instructions
	}
	if s.ChatModel != "" {
		c.llmModel = s.ChatModel
	}
	// The ear is hearing's session's: what is asked once it is open is kept
	// for nothing, and said so.
	if t := s.Audio.Input.Transcription; t != nil {
		if c.hearing == "" {
			c.sttModel, c.sttPrompt = t.Model, t.Prompt
			if t.Language != "" {
				c.language = t.Language
			}
		} else if t.Model != c.sttModel || t.Prompt != c.sttPrompt {
			c.log.Info("ear asked for once the call was taken, kept", "asked", t.Model, "ear", c.sttModel)
		}
	}
	if v := s.Audio.Output.Voice; v != "" && c.speaking == "" {
		if strings.Contains(v, "_") {
			c.voice = v
		} else {
			c.log.Info("voice not ours, kept", "asked", v, "voice", c.voice)
		}
	}
	switch td := s.Audio.Input.TurnDetection; {
	case len(td) == 0:
	case string(td) == "null":
		c.createResponse, c.interrupt = false, false
	default:
		var t struct {
			CreateResponse    *bool `json:"create_response"`
			InterruptResponse *bool `json:"interrupt_response"`
			SilenceMs         int   `json:"silence_duration_ms"`
			PrefixPaddingMs   *int  `json:"prefix_padding_ms"`
		}
		if json.Unmarshal(td, &t) == nil {
			c.createResponse = t.CreateResponse == nil || *t.CreateResponse
			c.interrupt = t.InterruptResponse == nil || *t.InterruptResponse
			if c.hearing == "" {
				c.mergeMs, c.prerollMs = t.SilenceMs, t.PrefixPaddingMs
			}
		}
	}
}

func (c *Call) session() map[string]any {
	td := any(nil)
	if c.createResponse || c.interrupt {
		td = map[string]any{"type": "server_vad", "create_response": c.createResponse, "interrupt_response": c.interrupt}
	}
	pcm := map[string]any{"type": "audio/pcm", "rate": audio.Rate}
	return map[string]any{
		"type": "realtime", "id": c.id, "model": c.model,
		"output_modalities": []string{"audio"}, "instructions": c.instructions, "chat_model": c.llmModel,
		"audio": map[string]any{
			"input":  map[string]any{"format": pcm, "transcription": map[string]any{"model": c.sttModel, "language": c.language, "prompt": c.sttPrompt}, "turn_detection": td},
			"output": map[string]any{"format": pcm, "voice": c.voice},
		},
	}
}

func (c *Call) create(ctx context.Context, raw json.RawMessage, eventID string) {
	var it struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &it) != nil || it.Type != "message" {
		c.send(ctx, errorEvent("invalid_request_error", eventID, "only message items are carried"))
		return
	}
	var text []string
	for _, p := range it.Content {
		if p.Text != "" {
			text = append(text, p.Text)
		}
	}
	if it.ID == "" {
		it.ID = "item_" + short()
	}
	c.add(ctx, &item{id: it.ID, role: it.Role, text: strings.Join(text, " ")})
}

func (c *Call) add(ctx context.Context, it *item) {
	prev := any(nil)
	if n := len(c.items); n > 0 {
		prev = c.items[n-1].id
	}
	c.items = append(c.items, it)
	c.send(ctx, map[string]any{"type": "conversation.item.added", "previous_item_id": prev, "item": it.wire("completed")})
	c.send(ctx, map[string]any{"type": "conversation.item.done", "previous_item_id": prev, "item": it.wire("completed")})
}

func (c *Call) find(id string) *item {
	for _, it := range c.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

func (c *Call) delete(ctx context.Context, id, eventID string) {
	for i, it := range c.items {
		if it.id == id {
			c.items = append(c.items[:i], c.items[i+1:]...)
			c.send(ctx, map[string]any{"type": "conversation.item.deleted", "item_id": id})
			return
		}
	}
	c.send(ctx, errorEvent("invalid_request_error", eventID, "no item "+id))
}

// truncate is the client saying how much of an answer was heard: the
// answer in the conversation becomes what was heard. An answer still being
// said is stopped first, and cut when speaking has said how far it got.
func (c *Call) truncate(ctx context.Context, id string, content, endMs int, eventID string) {
	if r := c.resp; r != nil && r.item == id {
		r.truncate = &endMs
		if r.reason == "" {
			c.cancel(ctx, "client_cancelled")
		}
		return
	}
	it := c.find(id)
	if it == nil || it.role != "assistant" {
		c.send(ctx, errorEvent("invalid_request_error", eventID, "no answer "+id+" to truncate"))
		return
	}
	c.cut(ctx, it, content, endMs)
}

func (c *Call) cut(ctx context.Context, it *item, content, endMs int) {
	was := it.text
	it.text = heard(it.pieces, endMs)
	c.log.Info("answer truncated", "item", it.id, "audio_end_ms", endMs, "said", len(was), "heard", it.text)
	c.send(ctx, map[string]any{"type": "conversation.item.truncated", "item_id": it.id, "content_index": content, "audio_end_ms": endMs})
}

// --- the organs' events

func (c *Call) organ(ctx context.Context, ev organs.Event) {
	c.log.Info("organ", "type", ev.Type, "session", ev.Session)
	switch ev.Type {
	case "speechrecog.start-of-input":
		c.user = "item_" + short()
		c.send(ctx, map[string]any{"type": "input_audio_buffer.speech_started", "item_id": c.user, "audio_start_ms": c.sinceFirst(ev.Time("started_at"))})
		if c.resp != nil && c.interrupt {
			c.cancel(ctx, "turn_detected")
		}
	case "speechrecog.recognition-complete":
		c.heard(ctx, ev)
	case "speechrecog.session-closed":
		c.log.Warn("hearing closed the session", "reason", ev.Str("reason"))
		c.send(ctx, errorEvent("server_error", "", "hearing closed the call's session: "+ev.Str("reason")))
		_ = c.ws.Close(websocket.StatusInternalError, "hearing is gone")
		c.hearing, c.stopped = "", true
	case "speechsynth.speech-marker":
		if ts, ok := ev.Uint32("rtp_timestamp"); ok {
			c.marked(ev.Str("request_id"), ts)
		}
	case "speechsynth.speak-complete":
		c.spoke(ctx, ev.Str("request_id"), ev.Str("completion_cause"), ev.Str("spoken"))
	case "speechsynth.session-closed":
		c.log.Warn("speaking closed the session", "reason", ev.Str("reason"))
		c.o.SpeakingEv.Unsubscribe(c.speaking)
		c.speaking = ""
		if c.receiver != nil {
			c.receiver.Close()
			c.receiver = nil
		}
		if r := c.resp; r != nil {
			for _, p := range r.pieces {
				if p.cause == "" {
					p.cause = "error"
				}
			}
			r.failure = "speaking closed the session: " + ev.Str("reason")
			c.finishIfDone(ctx)
		}
	}
}

// sinceFirst is t in ms of the call's sound, which is how the schema
// counts audio_start_ms and audio_end_ms.
func (c *Call) sinceFirst(t time.Time) int {
	c.firstSoundMu.Lock()
	first := c.firstSound
	c.firstSoundMu.Unlock()
	if t.IsZero() || first.IsZero() || t.Before(first) {
		return 0
	}
	return int(t.Sub(first) / time.Millisecond)
}

// heard is an utterance over: its words, and an answer to it.
func (c *Call) heard(ctx context.Context, ev organs.Event) {
	id := c.user
	if id == "" {
		id = "item_" + short()
	}
	c.user = ""
	prev := any(nil)
	if n := len(c.items); n > 0 {
		prev = c.items[n-1].id
	}
	c.send(ctx, map[string]any{"type": "input_audio_buffer.speech_stopped", "item_id": id, "audio_end_ms": c.sinceFirst(ev.Time("ended_at"))})
	c.send(ctx, map[string]any{"type": "input_audio_buffer.committed", "item_id": id, "previous_item_id": prev})
	it := &item{id: id, role: "user"}
	c.items = append(c.items, it)
	c.send(ctx, map[string]any{"type": "conversation.item.added", "previous_item_id": prev, "item": it.wire("in_progress")})
	text := strings.TrimSpace(ev.Str("text"))
	if ev.Str("completion_cause") == "recognizer-error" {
		c.send(ctx, map[string]any{"type": "conversation.item.input_audio_transcription.failed", "item_id": id, "content_index": 0,
			"error": map[string]any{"type": "transcription_error", "message": "the ear could not transcribe it"}})
	} else {
		it.text = text
		c.send(ctx, map[string]any{"type": "conversation.item.input_audio_transcription.completed", "item_id": id, "content_index": 0, "transcript": text})
	}
	c.send(ctx, map[string]any{"type": "conversation.item.done", "previous_item_id": prev, "item": it.wire("completed")})
	if text == "" || !c.createResponse {
		return
	}
	if c.resp != nil {
		// An answer still going: it ends (the reflex's word), and this
		// utterance gets its own after it.
		c.resp.next = true
		c.cancel(ctx, "turn_detected")
		return
	}
	c.answer(ctx)
}

// --- an answer

// answer starts one: the model is asked with the conversation so far, its
// words go to speaking a sentence at a time.
func (c *Call) answer(ctx context.Context) {
	r := &response{id: "resp_" + short(), item: "item_" + short()}
	c.resp = r
	c.send(ctx, map[string]any{"type": "response.created", "response": c.wireResponse(r, "in_progress", nil, nil)})
	msg := r.wireItem("in_progress", "")
	c.send(ctx, map[string]any{"type": "response.output_item.added", "response_id": r.id, "output_index": 0, "item": msg})
	c.send(ctx, map[string]any{"type": "conversation.item.added", "previous_item_id": c.lastID(), "item": msg})
	c.send(ctx, map[string]any{"type": "response.content_part.added", "response_id": r.id, "item_id": r.item, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "audio", "transcript": ""}})
	if c.speaking == "" {
		if err := c.openMouth(ctx); err != nil {
			c.log.Error("mouth not opened", "err", err)
			r.failure, r.written = "speaking: "+err.Error(), true
			c.finishIfDone(ctx)
			return
		}
	}
	msgs := []llm.Message{}
	if c.instructions != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: c.instructions})
	}
	for _, it := range c.items {
		if it.text != "" && (it.role == "user" || it.role == "assistant" || it.role == "system") {
			msgs = append(msgs, llm.Message{Role: it.role, Content: it.text})
		}
	}
	mctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	model := c.llmModel
	go func() {
		err := c.o.LLM.Stream(mctx, model, msgs, func(d string) {
			select {
			case c.in <- wrote{r.id, d}:
			case <-mctx.Done():
			}
		})
		select {
		case c.in <- written{r.id, err}:
		case <-ctx.Done():
		}
	}()
	c.log.Info("answering", "response", r.id, "model", model, "messages", len(msgs))
}

func (c *Call) lastID() any {
	if n := len(c.items); n > 0 {
		return c.items[n-1].id
	}
	return nil
}

func (c *Call) wrote(ctx context.Context, m wrote) {
	r := c.resp
	if r == nil || r.id != m.resp || r.reason != "" || r.failure != "" {
		return
	}
	for _, s := range r.phrases.feed(m.text) {
		c.say(ctx, r, s)
	}
}

func (c *Call) written(ctx context.Context, m written) {
	r := c.resp
	if r == nil || r.id != m.resp {
		return
	}
	r.written = true
	if r.reason == "" && r.failure == "" {
		if m.err != nil && ctx.Err() == nil {
			c.log.Error("the model failed", "err", m.err)
			r.failure = m.err.Error()
			c.send(ctx, errorEvent("server_error", "", m.err.Error()))
		}
		for _, s := range r.phrases.flush() {
			c.say(ctx, r, s)
		}
	}
	c.finishIfDone(ctx)
}

// say gives speaking one phrase; its words go to the client's transcript as
// they are given.
func (c *Call) say(ctx context.Context, r *response, s string) {
	delta := s
	if r.text != "" {
		delta = " " + s
	}
	var spoke struct {
		RequestID string `json:"request_id"`
	}
	if err := c.o.Speaking.Call(ctx, "speak", map[string]any{"session_id": c.speaking, "text": s}, &spoke); err != nil {
		c.log.Error("speak failed", "err", err)
		r.failure = "speaking: " + err.Error()
		if r.cancel != nil {
			r.cancel()
		}
		return
	}
	r.text += delta
	r.pieces = append(r.pieces, &piece{request: spoke.RequestID, text: s})
	c.send(ctx, map[string]any{"type": "response.output_audio_transcript.delta", "response_id": r.id, "item_id": r.item,
		"output_index": 0, "content_index": 0, "delta": delta})
}

// sound is speaking's sound coming back, sent to the client as the
// answer's. Where each packet begins in the answer's sound is kept by its
// timestamp, and a sentence marked at it is placed there.
func (c *Call) sound(ctx context.Context, pcm []byte, ts uint32) {
	r := c.resp
	if r == nil {
		return
	}
	pos := r.audioMs + len(r.pending)/bytesPerMs
	if r.at == nil {
		r.at = map[uint32]int{}
	}
	r.at[ts] = pos
	for _, p := range r.pieces {
		if p.marked && !p.placed && p.markTs == ts {
			p.startMs, p.placed = pos, true
		}
	}
	r.pending = append(r.pending, pcm...)
	if len(r.pending) >= deltaBytes {
		c.flushSound(ctx, r)
	}
}

func (c *Call) flushSound(ctx context.Context, r *response) {
	if len(r.pending) == 0 {
		return
	}
	r.audioMs += len(r.pending) / bytesPerMs
	c.sendQuiet(ctx, map[string]any{"type": "response.output_audio.delta", "response_id": r.id, "item_id": r.item,
		"output_index": 0, "content_index": 0, "delta": base64.StdEncoding.EncodeToString(r.pending)})
	r.pending = r.pending[:0]
}

// marked is speaking's marker: a phrase's first sentence began at ts in its
// stream. Placed now if that packet has come, else when it comes (the event
// and the sound travel apart).
func (c *Call) marked(request string, ts uint32) {
	r := c.resp
	if r == nil {
		return
	}
	for _, p := range r.pieces {
		if p.request != request || p.marked {
			continue
		}
		p.marked, p.markTs = true, ts
		if pos, ok := r.at[ts]; ok {
			p.startMs, p.placed = pos, true
		}
		return
	}
}

// spoke is speaking's word on one phrase: what went out, and why it ended.
func (c *Call) spoke(ctx context.Context, request, cause, spoken string) {
	r := c.resp
	if r == nil {
		return
	}
	start := 0
	for _, p := range r.pieces {
		if p.request != request {
			if p.cause != "" {
				start = p.endMs
			}
			continue
		}
		c.flushSound(ctx, r)
		p.cause, p.spoken, p.endMs = cause, spoken, r.audioMs
		if !p.placed {
			p.startMs = start
		}
		if cause == "error" && r.failure == "" {
			r.failure = "speaking could not say: " + p.text
		}
		break
	}
	c.finishIfDone(ctx)
}

// cancel ends the answer being made: the model stops writing, speaking
// stops saying. turn_detected is the caller speaking over it (speaking's
// barge-in), client_cancelled the client's word (speaking's stop).
func (c *Call) cancel(ctx context.Context, reason string) {
	r := c.resp
	if r == nil || r.reason != "" {
		return
	}
	r.reason = reason
	if r.cancel != nil {
		r.cancel()
	}
	busy := false
	for _, p := range r.pieces {
		busy = busy || p.cause == ""
	}
	if busy && c.speaking != "" {
		tool := "stop"
		if reason == "turn_detected" {
			tool = "barge_in_occurred"
		}
		if err := c.o.Speaking.Call(ctx, tool, map[string]any{"session_id": c.speaking}, nil); err != nil {
			c.log.Error("speaking not stopped", "err", err)
		}
	}
	c.log.Info("answer cancelled", "response", r.id, "reason", reason)
	c.finishIfDone(ctx)
}

// finishIfDone ends the answer once the model has finished writing and
// speaking has reported every phrase.
func (c *Call) finishIfDone(ctx context.Context) {
	r := c.resp
	if r == nil || !r.written && r.reason == "" {
		return
	}
	for _, p := range r.pieces {
		if p.cause == "" {
			return
		}
	}
	if !r.written {
		// cancelled while the model wrote: its end will not come here
		r.written = true
	}
	c.flushSound(ctx, r)
	c.resp = nil

	said := make([]string, 0, len(r.pieces))
	cut := false
	for _, p := range r.pieces {
		if p.cause == "normal" {
			said = append(said, p.text)
			continue
		}
		cut = true
		if p.spoken != "" {
			said = append(said, p.spoken)
		}
	}
	transcript := strings.Join(said, " ")
	it := &item{id: r.item, role: "assistant", text: transcript, pieces: r.pieces}
	c.items = append(c.items, it)

	status, details := "completed", map[string]any(nil)
	switch {
	case r.reason != "":
		status, details = "cancelled", map[string]any{"type": "cancelled", "reason": r.reason}
	case r.failure != "":
		status, details = "failed", map[string]any{"type": "failed", "error": map[string]any{"type": "server_error", "message": r.failure}}
	case cut:
		status, details = "incomplete", map[string]any{"type": "incomplete", "reason": "speaking"}
	}
	at := map[string]any{"response_id": r.id, "item_id": r.item, "output_index": 0, "content_index": 0}
	part := map[string]any{"type": "audio", "transcript": transcript}
	itemStatus := "incomplete"
	if status == "completed" {
		itemStatus = "completed"
		c.send(ctx, merge(at, map[string]any{"type": "response.output_audio.done"}))
		c.send(ctx, merge(at, map[string]any{"type": "response.output_audio_transcript.done", "transcript": transcript}))
		c.send(ctx, merge(at, map[string]any{"type": "response.content_part.done", "part": part}))
	}
	done := r.wireItem(itemStatus, transcript)
	c.send(ctx, map[string]any{"type": "response.output_item.done", "response_id": r.id, "output_index": 0, "item": done})
	c.send(ctx, map[string]any{"type": "conversation.item.done", "item": done})
	c.send(ctx, map[string]any{"type": "response.done", "response": c.wireResponse(r, status, details, []any{done})})
	c.log.Info("answered", "response", r.id, "status", status, "phrases", len(r.pieces), "audio_ms", r.audioMs, "said", transcript)
	if r.truncate != nil {
		c.cut(ctx, it, 0, *r.truncate)
	}
	if r.next {
		c.answer(ctx)
	}
}

// --- the wire

func (c *Call) wireResponse(r *response, status string, details map[string]any, output []any) map[string]any {
	if output == nil {
		output = []any{}
	}
	return map[string]any{
		"id": r.id, "status": status, "status_details": details,
		"output": output, "conversation_id": c.conv, "output_modalities": []string{"audio"},
		"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": audio.Rate}, "voice": c.voice}},
	}
}

func (r *response) wireItem(status, transcript string) map[string]any {
	content := []any{}
	if status != "in_progress" {
		content = append(content, map[string]any{"type": "output_audio", "transcript": transcript})
	}
	return map[string]any{"id": r.item, "type": "message", "status": status, "role": "assistant", "content": content}
}

func (it *item) wire(status string) map[string]any {
	var content []any
	switch it.role {
	case "user":
		content = []any{map[string]any{"type": "input_audio", "transcript": it.text}}
	case "assistant":
		content = []any{map[string]any{"type": "output_audio", "transcript": it.text}}
	default:
		content = []any{map[string]any{"type": "input_text", "text": it.text}}
	}
	return map[string]any{"id": it.id, "type": "message", "status": status, "role": it.role, "content": content}
}

func errorEvent(kind, eventID, message string) map[string]any {
	e := map[string]any{"type": kind, "message": message}
	if eventID != "" {
		e["event_id"] = eventID
	}
	return map[string]any{"type": "error", "error": e}
}

func merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// send writes one event to the client, logged.
func (c *Call) send(ctx context.Context, ev map[string]any) {
	c.log.Info("event", "dir", "out", "type", ev["type"])
	c.sendQuiet(ctx, ev)
}

// sendQuiet writes one event unlogged: the sound, fifty times a second.
func (c *Call) sendQuiet(ctx context.Context, ev map[string]any) {
	ev["event_id"] = "event_" + short()
	b, err := json.Marshal(ev)
	if err != nil {
		c.log.Error("event not encoded", "err", err)
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.ws.Write(wctx, websocket.MessageText, b); err != nil && ctx.Err() == nil {
		c.log.Warn("event not sent", "type", ev["type"], "err", err)
	}
}

func short() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:20] }
