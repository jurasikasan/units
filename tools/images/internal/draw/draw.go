// Package draw is a tool that draws: a prompt in, a picture out, asked of
// any endpoint that speaks OpenAI's Images API (a router such as LiteLLM,
// or one engine), offered to whoever calls it as one MCP tool.
//
// What it returns is the tool's: a picture and a few words saying what was
// drawn. Who called it, and what they make of it, is not its business.
package draw

import (
	"bytes"
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

// Config is where pictures are asked for and which models draw them.
type Config struct {
	URL       string // the Images API's base, with /v1: http://litellm:4000/v1
	Key       string // sent as a Bearer token when set
	Model     string // the model asked by default
	FastModel string // the model asked when the call wants it fast; Model when empty
	Timeout   time.Duration
}

// Images asks an OpenAI Images endpoint for pictures.
type Images struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) *Images {
	if cfg.FastModel == "" {
		cfg.FastModel = cfg.Model
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &Images{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

// Picture is what came back: the bytes, their media type, and what drew it.
type Picture struct {
	Data     []byte
	MIMEType string
	Model    string
	Size     string // as the endpoint said it, when it did
	Revised  string // the prompt as the model rewrote it, when it did
}

// Draw asks for one picture. size is OpenAI's ("1024x1024", "auto"), empty
// for the model's own; fast picks the fast model.
func (im *Images) Draw(ctx context.Context, prompt, size string, fast bool) (*Picture, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("a prompt is needed: what to draw")
	}
	model := im.cfg.Model
	if fast {
		model = im.cfg.FastModel
	}
	body := map[string]any{"model": model, "prompt": prompt, "n": 1}
	if size != "" {
		body["size"] = size
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(im.cfg.URL, "/")+"/images/generations", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if im.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+im.cfg.Key)
	}
	resp, err := im.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the images endpoint did not answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("the images endpoint's answer broke off: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the images endpoint answered %d: %s", resp.StatusCode, refusal(raw))
	}
	var out struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Size string `json:"size"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Data) == 0 {
		return nil, errors.New("the images endpoint answered with no picture")
	}
	d := out.Data[0]
	var data []byte
	switch {
	case d.B64JSON != "":
		data, err = base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("the picture is not base64: %w", err)
		}
	case d.URL != "":
		data, err = im.fetch(ctx, d.URL)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("the images endpoint answered with no picture")
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return nil, fmt.Errorf("what came back is not a picture (%s)", mime)
	}
	return &Picture{Data: data, MIMEType: mime, Model: model, Size: out.Size, Revised: d.RevisedPrompt}, nil
}

// fetch reads a picture an endpoint answered with as a URL.
func (im *Images) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := im.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the picture's url did not answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the picture's url answered %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// refusal is an OpenAI error's message, else the body as it came, short.
func refusal(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// In is draw's arguments.
type In struct {
	Prompt string `json:"prompt" jsonschema:"what to draw, in words: the subject, the style, the light"`
	Size   string `json:"size,omitempty" jsonschema:"WIDTHxHEIGHT in pixels, e.g. 1024x768; left out, the model's own"`
	Fast   bool   `json:"fast,omitempty" jsonschema:"true for a quicker, rougher picture"`
}

// NewMCPServer offers draw as an MCP tool.
func NewMCPServer(im *Images, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "images", Version: version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name: "draw",
		Description: "Draw a picture from a description in words. Returns the picture and a line saying what was drawn. " +
			"size is WIDTHxHEIGHT (e.g. 1024x768), left out for the model's own; fast=true for a quicker, rougher one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		p, err := im.Draw(ctx, in.Prompt, in.Size, in.Fast)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: Said(in.Prompt, p)},
			&mcp.ImageContent{Data: p.Data, MIMEType: p.MIMEType},
		}}, nil, nil
	})
	return s
}

// Said is the line beside the picture: what was drawn, by which model.
func Said(prompt string, p *Picture) string {
	what := prompt
	if p.Revised != "" {
		what = p.Revised
	}
	s := fmt.Sprintf("A picture (%s", p.MIMEType)
	if p.Size != "" {
		s += ", " + p.Size
	}
	return s + fmt.Sprintf(", by %s) of: %s", p.Model, what)
}
