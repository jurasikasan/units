package seat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// keepAlive is how long a watcher's stream may stay silent before a comment
// line goes out.
const keepAlive = 15 * time.Second

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// openAIError answers in OpenAI's error shape, as a model's door would.
func openAIError(w http.ResponseWriter, code int, typ, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": typ}})
}

// Handler serves the seat's walls (CONTRACT §2): the door, the seats' HTTP,
// MCP, GET /health.
func (s *Seats) Handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.door)
	mux.HandleFunc("GET /seats/{seat}/turns", func(w http.ResponseWriter, r *http.Request) {
		out := s.Pending(r.PathValue("seat"))
		if out == nil {
			out = []*Turn{}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /seats/{seat}/events", s.events)
	mux.HandleFunc("POST /seats/{seat}/turns/{id}/answer", func(w http.ResponseWriter, r *http.Request) {
		var a Answer
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			http.Error(w, "bad_answer: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch err := s.Answer(r.PathValue("seat"), r.PathValue("id"), a); {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{"answered": true})
		case errors.Is(err, ErrAnswered):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, ErrUnknown):
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	})
	srv := s.MCPServer()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version, "counts": s.Counts.Snapshot()})
	})
	return mux
}

// door is POST /v1/chat/completions: the request held as a turn of the seat
// its model names, answered as a model would.
func (s *Seats) door(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r, 32<<20)
	if err != nil {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "not a JSON chat request: "+err.Error())
		return
	}
	if err := CheckName(req.Model); err != nil {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "model names the seat: "+err.Error())
		return
	}
	if req.Stream {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "stream is not offered: a seat answers whole")
		return
	}
	a, t, err := s.Ask(r.Context(), req.Model, raw)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, Completion(t, a))
	case r.Context().Err() != nil:
		// The caller is gone; nobody reads this.
	default:
		openAIError(w, http.StatusGatewayTimeout, "timeout", "the seat "+req.Model+" did not answer: "+err.Error())
	}
}

func readAll(r *http.Request, max int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("a request over %d bytes", max)
	}
	if len(raw) == 0 {
		return nil, errors.New("an empty request")
	}
	return raw, nil
}

// events is GET /seats/{seat}/events: server-sent events, `turn` (the turn,
// its request whole) for every turn held now and every one that comes, `gone`
// when one is answered, withdrawn or expired.
func (s *Seats) events(w http.ResponseWriter, r *http.Request) {
	seat := r.PathValue("seat")
	if err := CheckName(seat); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	ch, stop := s.Watch(seat)
	defer stop()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": seat ", seat, "\n\n")
	fl.Flush()
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case e := <-ch:
			var data any = map[string]any{"id": e.ID, "reason": e.Reason}
			if e.Kind == "turn" {
				data = e.Turn
			}
			raw, _ := json.Marshal(data)
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, raw); err != nil {
				return
			}
		}
		fl.Flush()
	}
}

type awaitIn struct {
	Seat        string `json:"seat" jsonschema:"the seat to sit in: the model name its caller uses"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for a turn, 1 to 600; default 60"`
}

type awaitOut struct {
	TurnID     string   `json:"turn_id"`
	Seat       string   `json:"seat"`
	Tools      []string `json:"tools"`
	ToolChoice string   `json:"tool_choice,omitempty"`
	Images     int      `json:"images"`
}

type answerIn struct {
	Seat      string         `json:"seat" jsonschema:"the seat the turn is in"`
	TurnID    string         `json:"turn_id" jsonschema:"the turn await_turn gave"`
	Text      string         `json:"text,omitempty" jsonschema:"what you think or say, as a model's message content"`
	Tool      string         `json:"tool,omitempty" jsonschema:"the one tool to call, by its name among the turn's tools"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema:"the tool's arguments, as its parameters schema says"`
}

type answerOut struct {
	Answered bool `json:"answered"`
}

// MCPServer is the seat for Claude (or any MCP client): await_turn gives the
// next turn as a model would get it, answer answers it.
func (s *Seats) MCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "seat", Version: Version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "await_turn",
		Description: "Sit in a seat and wait for its next turn: the request a model would get — the system prompt, " +
			"what is remembered, the new moment, its pictures, the tools you may call. Answer it with answer. " +
			"turn_id is empty when no turn came within the wait: call again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in awaitIn) (*mcp.CallToolResult, awaitOut, error) {
		if err := CheckName(in.Seat); err != nil {
			return nil, awaitOut{}, fmt.Errorf("bad_argument: %v", err)
		}
		wait := time.Duration(in.WaitSeconds) * time.Second
		if wait <= 0 {
			wait = 60 * time.Second
		}
		if wait > 600*time.Second {
			wait = 600 * time.Second
		}
		wctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		t := s.Next(wctx, in.Seat)
		if t == nil {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "No turn in seat " + in.Seat + " yet; call again."}}},
				awaitOut{Seat: in.Seat, Tools: []string{}}, nil
		}
		text, images, out := Render(t)
		content := []mcp.Content{&mcp.TextContent{Text: text}}
		for _, img := range images {
			content = append(content, img)
		}
		return &mcp.CallToolResult{Content: content}, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "answer",
		Description: "Answer a turn as the model would: text, and at most one tool call (required when the turn's tool_choice is required).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in answerIn) (*mcp.CallToolResult, answerOut, error) {
		a := Answer{Text: in.Text}
		if in.Tool != "" {
			args := []byte("{}")
			if in.Arguments != nil {
				args, _ = json.Marshal(in.Arguments)
			}
			a.ToolCall = &ToolCall{Name: in.Tool, Arguments: args}
		}
		if err := s.Answer(in.Seat, in.TurnID, a); err != nil {
			return nil, answerOut{}, err
		}
		return nil, answerOut{Answered: true}, nil
	})
	return srv
}

// chat is what Render reads of a request.
type chat struct {
	Messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`
	ToolChoice json.RawMessage `json:"tool_choice"`
	Tools      []struct {
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

// Render is a turn in words for a reader who is not a model — every message
// in order, each picture as "(picture n)" with the picture itself beside —
// and what await_turn answers in structure.
func Render(t *Turn) (string, []*mcp.ImageContent, awaitOut) {
	var c chat
	_ = json.Unmarshal(t.Request, &c)
	out := awaitOut{TurnID: t.ID, Seat: t.Seat, Tools: []string{}}
	_ = json.Unmarshal(c.ToolChoice, &out.ToolChoice)
	var b strings.Builder
	var images []*mcp.ImageContent
	fmt.Fprintf(&b, "Turn %s in seat %s: answer as the model would.\n", t.ID, t.Seat)
	for _, m := range c.Messages {
		head := m.Role
		if m.ToolCallID != "" {
			head += " (" + m.ToolCallID + ")"
		}
		fmt.Fprintf(&b, "\n--- %s ---\n", head)
		var str string
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		switch {
		case json.Unmarshal(m.Content, &str) == nil:
			b.WriteString(str + "\n")
		case json.Unmarshal(m.Content, &parts) == nil:
			for _, p := range parts {
				switch p.Type {
				case "text":
					b.WriteString(p.Text + "\n")
				case "image_url":
					if img := dataImage(p.ImageURL.URL); img != nil {
						images = append(images, img)
						fmt.Fprintf(&b, "(picture %d)\n", len(images))
					} else {
						fmt.Fprintf(&b, "(picture at %s)\n", p.ImageURL.URL)
					}
				}
			}
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "(called %s %s, id %s)\n", tc.Function.Name, tc.Function.Arguments, tc.ID)
		}
	}
	fmt.Fprintf(&b, "\n--- tools")
	if out.ToolChoice != "" {
		fmt.Fprintf(&b, " (tool_choice: %s)", out.ToolChoice)
	}
	b.WriteString(" ---\n")
	for _, tl := range c.Tools {
		out.Tools = append(out.Tools, tl.Function.Name)
		fmt.Fprintf(&b, "%s: %s\n  parameters: %s\n", tl.Function.Name, tl.Function.Description, string(tl.Function.Parameters))
	}
	out.Images = len(images)
	return b.String(), images, out
}

// dataImage reads a data: URL picture, or nil.
func dataImage(u string) *mcp.ImageContent {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return nil
	}
	meta, payload, ok := strings.Cut(rest, ",")
	mime, isB64 := strings.CutSuffix(meta, ";base64")
	if !ok || !isB64 {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	return &mcp.ImageContent{Data: raw, MIMEType: mime}
}
