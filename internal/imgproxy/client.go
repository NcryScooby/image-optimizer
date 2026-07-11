package imgproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client fetches transformed images from an imgproxy base URL.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Client for the given imgproxy base URL (e.g. http://imgproxy:8080).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Fetch GETs path from imgproxy and returns the response body bytes.
func (c *Client) Fetch(ctx context.Context, path string) ([]byte, error) {
	if c == nil || c.baseURL == "" {
		return nil, fmt.Errorf("imgproxy: client not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("imgproxy: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imgproxy: fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("imgproxy: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("imgproxy: status %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}
