package driver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// reply is a run's answer gathered as threads' blocks, in the order it was
// made: the avatar's act. Its words are its own; a tool call is its own
// act, and the call's result is the tool's — kept as the call's result
// (a tool_result block, media included), never as the avatar's words.
type reply struct {
	items  []*Block
	texts  map[string]*Block // a text message by its id
	open   *Block            // the text message being said
	calls  map[string]*Block // a tool call by its id
	argsOf map[string]*strings.Builder
	done   map[string]bool // calls a result answered
}

func newReply() *reply {
	return &reply{texts: map[string]*Block{}, calls: map[string]*Block{}, argsOf: map[string]*strings.Builder{}, done: map[string]bool{}}
}

func (r *reply) start(id string) {
	b := &Block{Type: "text"}
	r.items = append(r.items, b)
	r.texts[id] = b
	r.open = b
}

func (r *reply) words(id, delta string) {
	b, ok := r.texts[id]
	if !ok {
		b = r.open
	}
	if b == nil {
		r.start(id)
		b = r.open
	}
	b.Text += delta
}

func (r *reply) call(id, name string) {
	b := &Block{Type: "tool_call", ID: id, Name: name}
	r.items = append(r.items, b)
	r.calls[id] = b
	r.argsOf[id] = &strings.Builder{}
	r.open = nil
}

func (r *reply) args(id, delta string) {
	if a, ok := r.argsOf[id]; ok {
		a.WriteString(delta)
	}
}

// result is a call's TOOL_CALL_RESULT: content the words the model read,
// raw the tool's own result as MCP gave it (minds/assistant's rawEvent).
// What the tool returned is kept: its text blocks as the words, its
// pictures and sounds as media, bytes as data: URLs with their sha256
// (until UNIT-68 gives them a store). Without raw, content is the words.
func (r *reply) result(callID, content string, raw any) {
	if _, ok := r.calls[callID]; !ok {
		return // a result of no call made here: nothing to answer
	}
	b := &Block{Type: "tool_result", CallID: callID, Text: content}
	if res, ok := toolResult(raw); ok {
		var words []string
		for _, c := range res.Content {
			switch c.Type {
			case "text":
				if c.Text != "" {
					words = append(words, c.Text)
				}
			case "image", "audio":
				if m, ok := media(c.Type, c.MimeType, c.Data); ok {
					b.Media = append(b.Media, m)
				}
			}
		}
		b.IsError = res.IsError
		if len(words) > 0 || len(b.Media) > 0 {
			b.Text = strings.Join(words, "\n")
		}
	}
	r.items = append(r.items, b)
	r.done[callID] = true
	r.open = nil
}

// mcpResult is the part of MCP's CallToolResult kept here.
type mcpResult struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func toolResult(raw any) (mcpResult, bool) {
	var res mcpResult
	if raw == nil {
		return res, false
	}
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &res) != nil {
		return res, false
	}
	return res, true
}

// media is a picture or a sound a tool returned, as threads' block: its
// bytes as a data: URL, their digest beside.
func media(kind, mime, data string) (Block, bool) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) == 0 || mime == "" {
		return Block{}, false
	}
	sum := sha256.Sum256(raw)
	return Block{Type: kind, URL: "data:" + mime + ";base64," + data, MediaType: mime, SHA256: hex.EncodeToString(sum[:])}, true
}

// blocks is the reply as it is journaled: text messages that said words,
// tool calls that were answered with their arguments (a JSON object; what
// is not one is kept as {"arguments": "<as it came>"}), and the results.
// A call no result answered is left out, as is its absence of an act.
func (r *reply) blocks() []Block {
	var out []Block
	for _, b := range r.items {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
		case "tool_call":
			if !r.done[b.ID] {
				continue
			}
			b.Args = object(r.argsOf[b.ID].String())
		}
		out = append(out, *b)
	}
	return out
}

func object(args string) json.RawMessage {
	t := bytes.TrimSpace([]byte(args))
	if len(t) == 0 {
		return json.RawMessage(`{}`)
	}
	var m map[string]any
	if t[0] == '{' && json.Unmarshal(t, &m) == nil {
		return json.RawMessage(t)
	}
	b, _ := json.Marshal(map[string]string{"arguments": args})
	return b
}
