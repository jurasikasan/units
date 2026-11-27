// Package vision has a frame put into words through OpenAI's
// POST /v1/chat/completions with an image part: a router (LiteLLM) or one
// engine with a vision model behind it.
package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultPrompt asks for the scene in words and, on a line of its own, the
// text read in it. A deployment or a session may say it otherwise, as long as
// the answer keeps that shape.
const DefaultPrompt = "Say what is seen in this picture in one or two plain sentences, as a person says what they see. " +
	"If any text can be read in it, add one last line that starts with \"Read:\" followed by the text exactly as written; " +
	"if nothing can be read, leave that line out."

// readPrefix starts the line of the answer that is the text read.
const readPrefix = "read:"

// Result is a frame in words: the scene, and the text read in it ("" when none).
type Result struct {
	Text string
	Read string
}

// Door is where frames go: an OpenAI-shaped base (…/v1), its bearer (a
// router's key; empty for an engine), the model and the prompt.
type Door struct {
	URL, Key, Model string
	// Prompt replaces DefaultPrompt when not empty.
	Prompt string
}

// Describe sends frame (of mediaType, image/jpeg or image/png) as a data URL
// in one user message, with language (ISO 639-1) as the language to answer in
// when not empty. Any transport or HTTP failure is an error; the caller
// reports the observation with recognizer-error.
func Describe(ctx context.Context, d Door, client *http.Client, frame []byte, mediaType, language string) (Result, error) {
	prompt := d.Prompt
	if prompt == "" {
		prompt = DefaultPrompt
	}
	if language != "" {
		prompt += " Answer in the language whose ISO 639-1 code is " + language + "."
	}
	body, err := json.Marshal(map[string]any{
		"model": d.Model,
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": prompt},
				map[string]any{"type": "image_url", "image_url": map[string]string{
					"url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(frame),
				}},
			},
		}},
	})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.Key != "" {
		req.Header.Set("Authorization", "Bearer "+d.Key)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("vision %s: status %d: %s", d.Model, resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("vision %s: bad body: %w", d.Model, err)
	}
	if len(out.Choices) == 0 {
		return Result{}, fmt.Errorf("vision %s: no choices", d.Model)
	}
	return Split(out.Choices[0].Message.Content), nil
}

// Split takes the model's answer apart: a line that starts with "Read:" is
// the text read, the rest is the scene. "Read: none" and the like are nothing read.
func Split(answer string) Result {
	var scene []string
	var r Result
	for _, line := range strings.Split(answer, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), readPrefix) {
			read := strings.TrimSpace(line[len(readPrefix):])
			switch strings.ToLower(strings.Trim(read, ".\"'")) {
			case "", "none", "nothing", "n/a", "-":
			default:
				r.Read = strings.Trim(read, "\"")
			}
			continue
		}
		scene = append(scene, line)
	}
	r.Text = strings.Join(scene, " ")
	return r
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
