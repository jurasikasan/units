package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

type HTTPClientWrapper struct {
	client *http.Client
}

const defaultTimeout = 30 * time.Second

func NewHTTPClientWrapper() *HTTPClientWrapper {
	return &HTTPClientWrapper{
		client: &http.Client{Timeout: defaultTimeout},
	}
}

func (h *HTTPClientWrapper) GetHTTPClient() *http.Client {
	return h.client
}

func (h *HTTPClientWrapper) Get(url string) (*http.Response, error) {
	return h.client.Get(url)
}

func (h *HTTPClientWrapper) DoRequest(method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (h *HTTPClientWrapper) DoRequestWithJSON(method, url string, data interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if data != nil {
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("Error marshalling data: %v", err)
			return nil, err
		}
		bodyReader = bytes.NewBuffer(jsonData)
	}

	headers := map[string]string{
		"Content-Type": "application/json",
	}

	return h.DoRequest(method, url, bodyReader, headers)
}

func (h *HTTPClientWrapper) HandleResponse(resp *http.Response) error {
	return h.handleResponse(resp, true)
}

func (h *HTTPClientWrapper) handleResponse(resp *http.Response, drainOnSuccess bool) error {

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		return &HttpError{Code: resp.StatusCode, Message: string(body)}
	}
	if drainOnSuccess {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return nil
}

func (h *HTTPClientWrapper) HandleResponseWithDecode(resp *http.Response, result interface{}) error {
	if err := h.handleResponse(resp, false); err != nil {
		return err
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			log.Printf("Failed to decode response body: %v", err)
			return err
		}
	}

	return nil
}
