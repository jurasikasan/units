package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// The three doors out of the brain, each spoken in its owner's own terms:
// the model by OpenAI's chat completions, frames by artifact_store's read by
// sha256, memory by threads' REST API. Nothing else crosses.

// Model is an OpenAI-shaped chat door (a router such as LiteLLM, or one
// engine), spoken through OpenAI's own SDK: its base (…/v1), its bearer,
// and the model to ask.
type Model struct {
	URL, Key, Name string
	Client         *http.Client
}

// Answer is the first choice of a completion: its words, and the tool calls
// it made, as the model gave them.
type Answer struct {
	Text  string
	Calls []openai.ChatCompletionMessageToolCallUnion
}

// Think asks for one chat completion and gives the first choice. With tools,
// the model is offered them and no tool_choice is sent (auto): not every
// model takes "required" (some hang on it, some ignore it), so a model's
// words without a call are read by the brain instead (CONTRACT §2.5). The
// SDK does not retry: a thought that failed is the tick's to report.
func (m Model) Think(ctx context.Context, msgs []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (Answer, error) {
	opts := []option.RequestOption{option.WithBaseURL(strings.TrimRight(m.URL, "/") + "/"), option.WithMaxRetries(0)}
	if m.Key != "" {
		opts = append(opts, option.WithAPIKey(m.Key))
	} else {
		opts = append(opts, option.WithAPIKey(""), option.WithHeaderDel("Authorization"))
	}
	if m.Client != nil {
		opts = append(opts, option.WithHTTPClient(m.Client))
	}
	client := openai.NewClient(opts...)
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(m.Name),
		Messages: msgs,
	}
	if len(tools) > 0 {
		params.Tools = tools
	}
	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Answer{}, fmt.Errorf("model %s: %w", m.Name, err)
	}
	if len(resp.Choices) == 0 {
		return Answer{}, fmt.Errorf("model %s: no choices", m.Name)
	}
	msg := resp.Choices[0].Message
	return Answer{Text: strings.TrimSpace(msg.Content), Calls: msg.ToolCalls}, nil
}

// Store is artifact_store, read by content only.
type Store struct {
	URL    string
	Client *http.Client
}

// maxFrame bounds what one frame may weigh in memory: a frame is a still,
// not a file.
const maxFrame = 16 << 20

// RefURL is the content-addressed url of a frame, as written into memory:
// immutable, the bytes' own digest in it.
func (s Store) RefURL(sha string) string {
	return strings.TrimRight(s.URL, "/") + "/v1/_sha256/" + sha
}

// Fetch reads GET {URL}/v1/_sha256/{sha} and checks the bytes are the ones
// the digest names.
func (s Store) Fetch(ctx context.Context, sha string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.RefURL(sha), nil)
	if err != nil {
		return nil, err
	}
	raw, err := do(s.Client, req, maxFrame)
	if err != nil {
		return nil, fmt.Errorf("artifact_store %s: %w", sha, err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("artifact_store %s: the bytes answered are not the ones named", sha)
	}
	return raw, nil
}

// do sends req and gives a 2xx body of at most max bytes.
func do(client *http.Client, req *http.Request, max int64) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("answer over %d bytes", max)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
