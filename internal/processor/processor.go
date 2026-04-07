// Package processor defines the SQS message schema and orchestrates
// parsing + forwarding to the Seerr API.
package processor

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
	MediaType MediaType `json:"mediaType"`        // "movie" | "tv" | "anime"
	Title     string    `json:"title"`            // human-readable title (for logging)
	TmdbID    int       `json:"tmdbId"`           // The Movie Database ID (required for movies/TV)
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

// Process is invoked by the SQS consumer for every received message.
//
// TODO: replace the hardcoded stub below with real message parsing once the
// SQS message schema is confirmed.
func (p *Processor) Process(ctx context.Context, body string) error {
	p.log.Info("received SQS message body (stub – body ignored for now)",
		slog.String("body", body),
	)

	// ── STUB: use a hardcoded request instead of parsing the message body ─────
	req := hardcodedRockyRequest()
	// ── END STUB ──────────────────────────────────────────────────────────────

	return p.submit(ctx, req)
}

// ProcessHardcoded sends the hardcoded Rocky movie request directly to Seerr,
// bypassing SQS entirely. Useful for smoke-testing the Seerr integration.
func (p *Processor) ProcessHardcoded(ctx context.Context) error {
	return p.submit(ctx, hardcodedRockyRequest())
}

// submit validates a MediaRequest and forwards it to the Seerr API.
func (p *Processor) submit(ctx context.Context, req *MediaRequest) error {
	ctx, span := otel.Tracer("seerr-service").Start(ctx, "processor.submit",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("media.type", string(req.MediaType)),
			attribute.String("media.title", req.Title),
			attribute.Int("media.tmdb_id", req.TmdbID),
		),
	)
	defer span.End()

	p.log.InfoContext(ctx, "submitting media request",
		slog.String("mediaType", string(req.MediaType)),
		slog.String("title", req.Title),
		slog.Int("tmdbId", req.TmdbID),
	)

	if err := p.validate(req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("invalid media request: %w", err)
	}

	seerrReq := p.toSeerrRequest(req)

	resp, err := p.seerrClient.CreateRequest(ctx, seerrReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("seerr request failed: %w", err)
	}

	span.SetAttributes(attribute.Int("seerr.request_id", resp.ID))
	p.log.InfoContext(ctx, "seerr request created",
		slog.Int("requestId", resp.ID),
		slog.String("status", resp.Status),
	)

	return nil
}

// hardcodedRockyRequest returns a stub MediaRequest for the movie "Rocky" (1976).
// TMDB ID 1366 – https://www.themoviedb.org/movie/1366
//
// TODO: remove once real SQS message parsing is implemented.
func hardcodedRockyRequest() *MediaRequest {
	return &MediaRequest{
		MediaType: MediaTypeMovie,
		Title:     "Rocky",
		TmdbID:    1366,
	}
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
		MediaType: mediaType,
		MediaID:   req.TmdbID,
		TvdbID:    req.TvdbID,
		Seasons:   req.Seasons,
		IsAnime:   req.MediaType == MediaTypeAnime,
	}
}
