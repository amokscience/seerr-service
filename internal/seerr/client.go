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
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	requestEndpoint = "/api/v1/request"
	searchEndpoint  = "/api/v1/search"
)

// ErrNoResults is returned by Search when the query produces no movie or TV results.
// It signals a permanent failure — retrying will not help.
var ErrNoResults = fmt.Errorf("no results found")

// SearchResult is a single item returned by GET /api/v1/search.
type SearchResult struct {
	ID        int    `json:"id"`
	MediaType string `json:"mediaType"` // "movie", "tv", or "person"
	Title     string `json:"title"`     // populated for movies
	Name      string `json:"name"`      // populated for TV shows
}

// DisplayName returns whichever of Title or Name is non-empty.
func (s *SearchResult) DisplayName() string {
	if s.Title != "" {
		return s.Title
	}
	return s.Name
}

// searchResponse is the envelope returned by GET /api/v1/search.
type searchResponse struct {
	Results []SearchResult `json:"results"`
}

// RequestPayload is the body sent to POST /api/v1/request.
type RequestPayload struct {
	MediaType         string `json:"mediaType"`                   // "movie" or "tv"
	MediaID           int    `json:"mediaId"`                     // TMDB ID
	TvdbID            int    `json:"tvdbId,omitempty"`            // optional TheTVDB ID
	Seasons           any    `json:"seasons,omitempty"`           // TV only: []int of season numbers or the string "all"
	Is4K              bool   `json:"is4k,omitempty"`              // request 4K version
	ServerID          int    `json:"serverId,omitempty"`          // Radarr/Sonarr server ID
	ProfileID         int    `json:"profileId,omitempty"`         // quality profile ID
	RootFolder        string `json:"rootFolder,omitempty"`        // download root folder
	LanguageProfileID int    `json:"languageProfileId,omitempty"` // Sonarr language profile
	UserID            int    `json:"userId,omitempty"`            // override requesting user
}

// RequestResponse is the subset of the response body we care about.
type RequestResponse struct {
	ID     int `json:"id"`
	Status int `json:"status"` // 1 = pending approval, 2 = approved, 3 = declined, 4 = available
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

	c.log.InfoContext(ctx, "sending request to Seerr API",
		slog.String("url", url),
		slog.String("mediaType", payload.MediaType),
		slog.Int("mediaId", payload.MediaID),
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

	c.log.InfoContext(ctx, "Seerr API response received",
		slog.Int("statusCode", resp.StatusCode),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("seerr returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result RequestResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// Search queries Seerr for the given title and returns the first non-person
// result (i.e. the first "movie" or "tv" entry). Results are ordered by
// Seerr's relevance/popularity ranking so the first match is the best one.
// Returns an error if no results are found or the HTTP call fails.
func (c *Client) Search(ctx context.Context, query string) (*SearchResult, error) {
	u := c.baseURL + searchEndpoint + "?query=" + url.PathEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}
	c.setHeaders(req)

	c.log.InfoContext(ctx, "searching Seerr", slog.String("query", query))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned %d: %s", resp.StatusCode, string(respBody))
	}

	var sr searchResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return nil, fmt.Errorf("unmarshal search response: %w", err)
	}

	for i := range sr.Results {
		if sr.Results[i].MediaType == "movie" || sr.Results[i].MediaType == "tv" {
			c.log.InfoContext(ctx, "Seerr search matched",
				slog.Int("tmdbId", sr.Results[i].ID),
				slog.String("mediaType", sr.Results[i].MediaType),
				slog.String("title", sr.Results[i].DisplayName()),
			)
			return &sr.Results[i], nil
		}
	}

	return nil, fmt.Errorf("%w for %q", ErrNoResults, query)
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
