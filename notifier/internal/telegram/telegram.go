package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const defaultAPI = "https://api.telegram.org"

type Client struct {
	token  string
	chatID string
	base   string
	http   *http.Client
}

func New(token, chatID, base string) *Client {
	if base == "" {
		base = defaultAPI
	}
	return &Client{
		token:  token,
		chatID: chatID,
		base:   base,

		http: &http.Client{Timeout: 10 * time.Second},
	}
}

type sendResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) Send(ctx context.Context, text string) error {
	form := url.Values{}
	form.Set("chat_id", c.chatID)
	form.Set("text", text)
	form.Set("disable_web_page_preview", "true")

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", c.base, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {

		return fmt.Errorf("telegram request failed: %T", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if readErr != nil {
		return fmt.Errorf("read telegram response: %w", readErr)
	}

	var parsed sendResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("telegram returned %d with an unparseable body", resp.StatusCode)
	}
	if !parsed.OK {
		if parsed.Parameters.RetryAfter > 0 {
			return fmt.Errorf("telegram rate limited, retry after %ds: %s",
				parsed.Parameters.RetryAfter, parsed.Description)
		}
		return fmt.Errorf("telegram refused the message (%d): %s", resp.StatusCode, parsed.Description)
	}
	return nil
}
