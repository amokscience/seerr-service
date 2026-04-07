// Package processor parses SQS messages and orchestrates forwarding to the Seerr API.
package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/amokscience/seerr-service/internal/pushover"
	"github.com/amokscience/seerr-service/internal/seerr"
)

// message is the expected JSON structure of an SQS message body.
//
// {"name":"Rocky"}
type message struct {
	Name string `json:"name"` // title to search for in Seerr
}

// Processor orchestrates message processing.
type Processor struct {
	seerrClient    *seerr.Client
	pushoverClient *pushover.Client
	log            *slog.Logger
}

// New creates a new Processor.
func New(seerrClient *seerr.Client, pushoverClient *pushover.Client, log *slog.Logger) *Processor {
	return &Processor{
		seerrClient:    seerrClient,
		pushoverClient: pushoverClient,
		log:            log,
	}
}

// Process is invoked by the SQS consumer for every received message.
// On any error, a Pushover notification is sent before the error is returned.
func (p *Processor) Process(ctx context.Context, body string) error {
	ctx, span := otel.Tracer("seerr-service").Start(ctx, "processor.Process",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	err := p.process(ctx, span, body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (p *Processor) process(ctx context.Context, span trace.Span, body string) error {
	// 1. Parse the incoming message.
	var msg message
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		return p.notifyAndReturn(ctx, "", fmt.Errorf("parse message: %w", err))
	}
	if msg.Name == "" {
		return p.notifyAndReturn(ctx, "", fmt.Errorf("message missing required field: name"))
	}

	span.SetAttributes(attribute.String("media.name", msg.Name))
	p.log.InfoContext(ctx, "processing media request", slog.String("name", msg.Name))

	// 2. Resolve name to TMDB ID via Seerr search. Take the first result.
	result, err := p.seerrClient.Search(ctx, msg.Name)
	if err != nil {
		if errors.Is(err, seerr.ErrNoResults) {
			// No results = permanent failure; notify and delete the message.
			return p.notifyAndDelete(ctx, msg.Name, fmt.Errorf("no results found for %q", msg.Name))
		}
		return p.notifyAndReturn(ctx, msg.Name, fmt.Errorf("search %q: %w", msg.Name, err))
	}

	span.SetAttributes(
		attribute.Int("media.tmdb_id", result.ID),
		attribute.String("media.type", result.MediaType),
		attribute.String("media.title", result.DisplayName()),
	)

	// 3. Submit the request to Seerr.
	payload := &seerr.RequestPayload{
		MediaType: result.MediaType,
		MediaID:   result.ID,
	}

	resp, err := p.seerrClient.CreateRequest(ctx, payload)
	if err != nil {
		return p.notifyAndReturn(ctx, msg.Name, fmt.Errorf("create request for %q (tmdbId=%d): %w", result.DisplayName(), result.ID, err))
	}

	span.SetAttributes(attribute.Int("seerr.request_id", resp.ID))
	p.log.InfoContext(ctx, "seerr request created",
		slog.String("name", msg.Name),
		slog.String("matchedTitle", result.DisplayName()),
		slog.String("mediaType", result.MediaType),
		slog.Int("tmdbId", result.ID),
		slog.Int("requestId", resp.ID),
		slog.Int("status", resp.Status),
	)

	return nil
}

// notifyAndReturn sends a Pushover notification then returns the error so the
// SQS message stays in the queue for retry.
func (p *Processor) notifyAndReturn(ctx context.Context, name string, err error) error {
	p.notify(ctx, name, err)
	return err
}

// notifyAndDelete sends a Pushover notification then returns nil so the SQS
// message is deleted. Use for permanent failures where retrying is pointless.
func (p *Processor) notifyAndDelete(ctx context.Context, name string, err error) error {
	p.notify(ctx, name, err)
	return nil
}

func (p *Processor) notify(ctx context.Context, name string, err error) {
	title := "seerr-service error"
	var msg string
	if name != "" {
		title = fmt.Sprintf("Failed: %s", name)
		msg = fmt.Sprintf("Could not request \"%s\": %s", name, err.Error())
	} else {
		msg = fmt.Sprintf("Message processing error: %s", err.Error())
	}

	p.log.ErrorContext(ctx, "processing error", slog.String("error", err.Error()))
	p.pushoverClient.Notify(ctx, title, msg)
}
