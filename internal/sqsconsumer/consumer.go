// Package sqsconsumer provides a long-poll SQS consumer that delivers
// raw message bodies to a handler function.
package sqsconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/amokscience/seerr-service/internal/config"
)

// HandlerFunc processes a single SQS message body.
// Returning a non-nil error prevents the message from being deleted (it will
// become visible again after the visibility timeout).
type HandlerFunc func(ctx context.Context, body string) error

// Consumer polls an SQS queue and dispatches messages to a HandlerFunc.
type Consumer struct {
	client *sqs.Client
	cfg    *config.Config
	log    *slog.Logger
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

		if len(msgs) > 0 {
			c.log.Info("SQS messages received", slog.Int("count", len(msgs)))
		}

		for _, msg := range msgs {
			msgID := aws.ToString(msg.MessageId)

			// Poison-pill guard: discard messages that have been received too many
			// times to prevent a bad message from blocking the queue indefinitely.
			if rc := receiveCount(msg); rc > 10 {
				c.log.Error("discarding poison-pill message (ApproximateReceiveCount > 10)",
					slog.String("messageId", msgID),
					slog.Int("receiveCount", rc),
					slog.String("body", aws.ToString(msg.Body)),
				)
				if err := c.delete(ctx, msg); err != nil {
					c.log.Error("failed to delete poison-pill message",
						slog.String("messageId", msgID),
						slog.String("error", err.Error()),
					)
				}
				continue
			}

			if err := c.handle(ctx, msg, handler); err != nil {
				c.log.Error("failed to process SQS message",
					slog.String("messageId", msgID),
					slog.String("error", err.Error()),
				)
				// Leave the message in the queue; it will re-appear after VisibilityTimeout.
				continue
			}

			if err := c.delete(ctx, msg); err != nil {
				c.log.Error("failed to delete SQS message",
					slog.String("messageId", msgID),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// receiveCount returns the ApproximateReceiveCount attribute of a message, or 0
// if the attribute is absent or unparseable.
func receiveCount(msg types.Message) int {
	v, ok := msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// poll performs a long-poll ReceiveMessage call.
func (c *Consumer) poll(ctx context.Context) ([]types.Message, error) {
	out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(c.cfg.SQSQueueURL),
		MaxNumberOfMessages:   c.cfg.SQSMaxMessages,
		VisibilityTimeout:     c.cfg.SQSVisibilityTimeout,
		WaitTimeSeconds:       c.cfg.SQSWaitTimeSeconds,
		AttributeNames:        []types.QueueAttributeName{types.QueueAttributeNameAll},
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return nil, fmt.Errorf("ReceiveMessage: %w", err)
	}
	return out.Messages, nil
}

// handle invokes the HandlerFunc for a single message, wrapped in a trace span.
func (c *Consumer) handle(ctx context.Context, msg types.Message, handler HandlerFunc) error {
	msgID := aws.ToString(msg.MessageId)

	ctx, span := otel.Tracer("seerr-service").Start(ctx, "sqs.message.process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "aws_sqs"),
			attribute.String("messaging.destination", c.cfg.SQSQueueURL),
			attribute.String("messaging.message_id", msgID),
		),
	)
	defer span.End()

	body := aws.ToString(msg.Body)
	c.log.InfoContext(ctx, "SQS message received, beginning processing",
		slog.String("messageId", msgID),
		slog.Int("bodyLen", len(body)),
	)

	if err := handler(ctx, body); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// delete removes a successfully processed message from the queue.
func (c *Consumer) delete(ctx context.Context, msg types.Message) error {
	msgID := aws.ToString(msg.MessageId)
	_, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.SQSQueueURL),
		ReceiptHandle: msg.ReceiptHandle,
	})
	if err != nil {
		return fmt.Errorf("DeleteMessage: %w", err)
	}
	c.log.InfoContext(ctx, "SQS message deleted from queue",
		slog.String("messageId", msgID),
	)
	return nil
}
