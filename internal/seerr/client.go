// Package seerr provides a minimal HTTP client for the Overseerr / Jellyseerr API.
// API reference: https://api-docs.overseerr.dev
package seerr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	// TODO: verify path once target Seerr instance version is confirmed
	requestEndpoint = "/api/v1/request"
	searchEndpoint  = "/api/v1/search" // reserved for future use
)

// RequestPayload is the body sent to POST /api/v1/request.
// TODO: expand fields (e.g. serverId, profileId) once Seerr config is known.
type RequestPayload struct {
	MediaType string `json:"mediaType"` // "movie" or "tv"
	MediaID   int    `json:"mediaId"`   // TMDB ID
	TvdbID    int    `json:"tvdbId,omitempty"`
	Seasons   []int  `json:"seasons,omitempty"` // nil/empty = all seasons
	IsAnime   bool   `json:"is4k,omitempty"`    // TODO: confirm anime flag field name
}

// RequestResponse is the subset of the response body we care about.
// TODO: fill in additional fields from the Seerr API response as needed.
type RequestResponse struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
}

// Client is an HTTP client scoped to a single Seerr instance.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	log        *slog.Logger
}

// New creates a new Seerr Client.
func New(baseURL, apiKey string, log *slog.Logger) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		log: log,
	}
}

// CreateRequest submits a media request to Seerr.
func (c *Client) CreateRequest(ctx context.Context, payload *RequestPayload) (*RequestResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	url := c.baseURL + requestEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	c.setHeaders(req)

	c.log.Debug("calling seerr API",
		slog.String("url", url),
		slog.String("payload", string(body)),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("seerr returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result RequestResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// Ping verifies connectivity to the Seerr API.
// TODO: wire into the health check once auth details are confirmed.
func (c *Client) Ping(ctx context.Context) error {
	url := c.baseURL + "/api/v1/settings/main"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping returned unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
}
