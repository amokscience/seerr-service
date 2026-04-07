// Package sqsconsumer provides a long-poll SQS consumer that delivers
// raw message bodies to a handler function.
package sqsconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/amokscience/seerr-service/internal/config"
)

// HandlerFunc processes a single SQS message body.
// Returning a non-nil error prevents the message from being deleted (it will
// become visible again after the visibility timeout).
type HandlerFunc func(ctx context.Context, body string) error

// Consumer polls an SQS queue and dispatches messages to a HandlerFunc.
type Consumer struct {
	client   *sqs.Client
	cfg      *config.Config
	log      *slog.Logger
}

// New creates a Consumer using the provided AWS config.
func New(awsCfg aws.Config, cfg *config.Config, log *slog.Logger) *Consumer {
	return &Consumer{
		client: sqs.NewFromConfig(awsCfg),
		cfg:    cfg,
		log:    log,
	}
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context, handler HandlerFunc) error {
	c.log.Info("starting SQS consumer",
		slog.String("queue", c.cfg.SQSQueueURL),
	)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("SQS consumer shutting down")
			return ctx.Err()
		default:
		}

		msgs, err := c.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.log.Error("SQS poll error", slog.String("error", err.Error()))
			// Back off briefly to avoid a tight error loop.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.cfg.SQSPollingInterval):
			}
			continue
		}

		for _, msg := range msgs {
			if err := c.handle(ctx, msg, handler); err != nil {
				c.log.Error("failed to handle SQS message",
					slog.String("messageId", aws.ToString(msg.MessageId)),
					slog.String("error", err.Error()),
				)
				// Leave the message in the queue; it will re-appear after VisibilityTimeout.
				continue
			}

			if err := c.delete(ctx, msg); err != nil {
				c.log.Error("failed to delete SQS message",
					slog.String("messageId", aws.ToString(msg.MessageId)),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// poll performs a long-poll ReceiveMessage call.
func (c *Consumer) poll(ctx context.Context) ([]types.Message, error) {
	out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.cfg.SQSQueueURL),
		MaxNumberOfMessages: c.cfg.SQSMaxMessages,
		VisibilityTimeout:   c.cfg.SQSVisibilityTimeout,
		WaitTimeSeconds:     c.cfg.SQSWaitTimeSeconds,
		AttributeNames:      []types.QueueAttributeName{types.QueueAttributeNameAll},
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return nil, fmt.Errorf("ReceiveMessage: %w", err)
	}
	return out.Messages, nil
}

// handle invokes the HandlerFunc for a single message.
func (c *Consumer) handle(ctx context.Context, msg types.Message, handler HandlerFunc) error {
	body := aws.ToString(msg.Body)
	c.log.Debug("received SQS message",
		slog.String("messageId", aws.ToString(msg.MessageId)),
		slog.Int("bodyLen", len(body)),
	)
	return handler(ctx, body)
}

// delete removes a successfully processed message from the queue.
func (c *Consumer) delete(ctx context.Context, msg types.Message) error {
	_, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.SQSQueueURL),
		ReceiptHandle: msg.ReceiptHandle,
	})
	if err != nil {
		return fmt.Errorf("DeleteMessage: %w", err)
	}
	c.log.Debug("deleted SQS message",
		slog.String("messageId", aws.ToString(msg.MessageId)),
	)
	return nil
}
