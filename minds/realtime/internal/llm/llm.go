// Package llm asks an OpenAI-shaped chat endpoint (LiteLLM) for an answer,
// streamed: POST /v1/chat/completions with stream true, its words as they
// come.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is a chat message: role system, user or assistant, and its text.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client is one endpoint: its base without /v1, and its key when it wants
// one.
type Client struct {
	Base, Key string
	HTTP      *http.Client
}

// Stream asks model for the next message after msgs and calls delta with
// each piece of its words, in order. It returns when the answer has ended,
// failed, or ctx is done.
func (c *Client) Stream(ctx context.Context, model string, msgs []Message, delta func(string)) error {
	body, err := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Base, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("the model answered %d: %s", resp.StatusCode, errorText(b))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			return fmt.Errorf("the model: %s", chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				delta(ch.Delta.Content)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

func errorText(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(b))
}
