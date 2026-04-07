// Package pushover provides a notification client for the Pushover webhook API.
// The token is appended as a query parameter; the recipient is encoded in the webhook URL.
package pushover

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client sends notifications via a Pushover webhook URL.
type Client struct {
	webhookURL string // PUSHOVER_WEBHOOK_URL – e.g. https://api.pushover.net/1/webhooks/<key>.json
	token      string // PUSHOVER_TOKEN – application token
	httpClient *http.Client
	log        *slog.Logger
}

// New creates a new Client.
// webhookURL is the full Pushover webhook URL; token is the application token.
// If either is empty, Notify calls are skipped with a warning log.
func New(webhookURL, token string, log *slog.Logger) *Client {
	return &Client{
		webhookURL: webhookURL,
		token:      token,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}
}

type alert struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

type pushoverPayload struct {
	Alert alert `json:"alert"`
}

// Notify posts a notification to the Pushover webhook.
// Errors are logged but never propagated so a notification failure cannot
// block message processing.
func (c *Client) Notify(ctx context.Context, title, message string) {
	if c.webhookURL == "" || c.token == "" {
		c.log.WarnContext(ctx, "pushover not configured - skipping notification",
			slog.String("title", title),
		)
		return
	}

	body, err := json.Marshal(pushoverPayload{
		Alert: alert{
			Title:   title,
			Message: message,
		},
	})
	if err != nil {
		c.log.ErrorContext(ctx, "notify: failed to marshal payload", slog.String("error", err.Error()))
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL+"?token="+c.token, bytes.NewReader(body))
	if err != nil {
		c.log.ErrorContext(ctx, "notify: failed to build request", slog.String("error", err.Error()))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.ErrorContext(ctx, "notify: http error", slog.String("error", err.Error()))
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		c.log.ErrorContext(ctx, "notify: unexpected status",
			slog.Int("statusCode", resp.StatusCode),
			slog.String("body", string(respBody)),
		)
		return
	}

	c.log.InfoContext(ctx, "pushover notification sent", slog.String("title", title))
}
