// Package processor defines the SQS message schema and orchestrates
// parsing + forwarding to the Seerr API.
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/amokscience/seerr-service/internal/seerr"
)

// MediaType represents the kind of media being requested.
type MediaType string

const (
	MediaTypeMovie MediaType = "movie"
	MediaTypeTV    MediaType = "tv"    // includes anime series
	MediaTypeAnime MediaType = "anime" // treated as tv with an anime flag
)

// MediaRequest is the expected JSON structure of an SQS message body.
// TODO: adjust fields once the upstream message format is confirmed.
type MediaRequest struct {
	MediaType MediaType `json:"mediaType"` // "movie" | "tv" | "anime"
	Title     string    `json:"title"`     // human-readable title (for logging)
	TmdbID    int       `json:"tmdbId"`    // The Movie Database ID (required for movies/TV)
	TvdbID    int       `json:"tvdbId,omitempty"` // TheTVDB ID (optional supplement)
	// TV / anime specific
	Seasons []int `json:"seasons,omitempty"` // empty = request all seasons
	// TODO: add additional fields (e.g. user ID, priority) as needed
}

// Processor orchestrates message processing.
type Processor struct {
	seerrClient *seerr.Client
	log         *slog.Logger
}

// New creates a new Processor.
func New(client *seerr.Client, log *slog.Logger) *Processor {
	return &Processor{
		seerrClient: client,
		log:         log,
	}
}

// Process parses a raw SQS message body and submits the request to Seerr.
func (p *Processor) Process(ctx context.Context, body string) error {
	var req MediaRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return fmt.Errorf("failed to unmarshal message body: %w", err)
	}

	p.log.Info("processing media request",
		slog.String("mediaType", string(req.MediaType)),
		slog.String("title", req.Title),
		slog.Int("tmdbId", req.TmdbID),
	)

	if err := p.validate(&req); err != nil {
		return fmt.Errorf("invalid media request: %w", err)
	}

	seerrReq := p.toSeerrRequest(&req)

	resp, err := p.seerrClient.CreateRequest(ctx, seerrReq)
	if err != nil {
		return fmt.Errorf("seerr request failed: %w", err)
	}

	p.log.Info("seerr request created",
		slog.Int("requestId", resp.ID),
		slog.String("status", resp.Status),
	)

	return nil
}

// validate performs basic sanity checks on an incoming MediaRequest.
func (p *Processor) validate(req *MediaRequest) error {
	if req.TmdbID == 0 {
		return fmt.Errorf("tmdbId is required")
	}
	switch req.MediaType {
	case MediaTypeMovie, MediaTypeTV, MediaTypeAnime:
	default:
		return fmt.Errorf("unknown mediaType %q", req.MediaType)
	}
	return nil
}

// toSeerrRequest converts an internal MediaRequest into the Seerr API payload.
func (p *Processor) toSeerrRequest(req *MediaRequest) *seerr.RequestPayload {
	// Seerr treats everything with seasons as "tv"; anime is flagged separately.
	mediaType := string(req.MediaType)
	if req.MediaType == MediaTypeAnime {
		mediaType = "tv"
	}

	return &seerr.RequestPayload{
		MediaType:   mediaType,
		MediaID:     req.TmdbID,
		TvdbID:      req.TvdbID,
		Seasons:     req.Seasons,
		IsAnime:     req.MediaType == MediaTypeAnime,
	}
}
