package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// APIGWEnvelope is the shape we pass to the processor.
// Body is plain string (raw JSON payload from GitHub), headers are flattened.
type APIGWEnvelope struct {
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Handler is the minimal interface the processor must satisfy.
type Handler interface {
	HandleFromEnvelope(ctx context.Context, env APIGWEnvelope) (int, error)
}

// Worker polls SQS, converts messages to APIGWEnvelope, and dispatches to a Handler.
type Worker struct {
	Client             *awssqs.Client
	QueueURL           string
	MaxMessages        int32 // 1..10
	WaitTimeSeconds    int32 // 0..20
	VisibilityTimeout  int32 // seconds
	DeleteOn4xx        bool
	ExtendOnProcessing bool // (reserved for future use)

	Processor Handler
}

// Run starts a long-poll receive loop until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w.Client == nil || w.QueueURL == "" || w.Processor == nil {
		return errors.New("sqs.Worker: missing Client, QueueURL or Processor")
	}
	slog.Info("sqs.worker.start",
		"queue", w.QueueURL,
		"maxMessages", w.vOrDefault(w.MaxMessages, 10),
		"waitSeconds", w.vOrDefault(w.WaitTimeSeconds, 10),
		"visibility", w.vOrDefault(w.VisibilityTimeout, 120),
		"deleteOn4xx", w.DeleteOn4xx,
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("sqs.worker.stop", "reason", "context_done")
			return ctx.Err()
		default:
		}

		// Receive
		out, err := w.Client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(w.QueueURL),
			MaxNumberOfMessages:   w.vOrDefault(w.MaxMessages, 10),
			WaitTimeSeconds:       w.vOrDefault(w.WaitTimeSeconds, 10),
			VisibilityTimeout:     w.vOrDefault(w.VisibilityTimeout, 120),
			MessageAttributeNames: []string{"All"},
			// We don't need AttributeNames for queue or system attributes here.
		})
		if err != nil {
			slog.Error("sqs.receive.error", "err", err)
			// small backoff to avoid hot loop on persistent errors
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if len(out.Messages) == 0 {
			continue // long-poll timeout; loop again
		}

		for _, m := range out.Messages {
			// Defensive: every message should have ReceiptHandle
			if m.ReceiptHandle == nil {
				slog.Warn("sqs.message.missing_receipt_handle", "messageID", aws.ToString(m.MessageId))
				continue
			}

			// Build envelope.
			env, envErr := w.toEnvelope(m)
			if envErr != nil {
				slog.Error("sqs.message.bad_envelope", "err", envErr, "messageID", aws.ToString(m.MessageId))
				// If we cannot parse, delete the poison message to avoid blocking.
				_ = w.deleteMessage(ctx, aws.ToString(m.ReceiptHandle))
				continue
			}

			// Dispatch.
			code, procErr := w.Processor.HandleFromEnvelope(ctx, env)

			// Decide deletion based on status code and policy.
			shouldDelete := false
			switch {
			case code >= 200 && code < 300:
				shouldDelete = true
			case code >= 400 && code < 500:
				shouldDelete = w.DeleteOn4xx
			default:
				// 5xx or unknown -> keep for retry (visibility will expire)
			}

			if procErr != nil {
				slog.Warn("sqs.message.process_error",
					"status", code,
					"err", procErr,
					"delete", shouldDelete,
					"messageID", aws.ToString(m.MessageId),
				)
			} else {
				slog.Info("sqs.message.processed",
					"status", code,
					"delete", shouldDelete,
					"messageID", aws.ToString(m.MessageId),
				)
			}

			if shouldDelete {
				if derr := w.deleteMessage(ctx, aws.ToString(m.ReceiptHandle)); derr != nil {
					slog.Error("sqs.message.delete_error", "err", derr, "messageID", aws.ToString(m.MessageId))
				}
			}
		}
	}
}

func (w *Worker) toEnvelope(m types.Message) (APIGWEnvelope, error) {
	// We support two forms:
	// 1) Body itself is the APIGWEnvelope JSON: {"headers":{...},"body":"..."}
	// 2) Body is the raw GitHub payload, and headers are provided in MessageAttributes.
	//    MessageAttributes["X-GitHub-Event"], ["X-GitHub-Delivery"], ["X-Hub-Signature-256"], etc.
	var env APIGWEnvelope

	// Try to unmarshal directly into APIGWEnvelope.
	if body := aws.ToString(m.Body); body != "" {
		if json.Unmarshal([]byte(body), &env) == nil && len(env.Headers) > 0 {
			// Looks like an envelope already; return it.
			return env, nil
		}
		// Otherwise, treat Body as raw GH payload, wrap with headers from attributes.
		env.Body = body
	} else {
		return APIGWEnvelope{}, errors.New("empty SQS message body")
	}

	hdrs := map[string]string{}
	for k, v := range m.MessageAttributes {
		// Only StringValue is expected for our GH/ApiGW headers.
		if v.StringValue != nil {
			hdrs[k] = aws.ToString(v.StringValue)
		}
	}
	env.Headers = hdrs

	// Provide a bit of validation: we expect at least Event and Signature.
	if _, ok := env.Headers["X-GitHub-Event"]; !ok {
		return APIGWEnvelope{}, fmt.Errorf("missing required header %q", "X-GitHub-Event")
	}
	if _, ok := env.Headers["X-Hub-Signature-256"]; !ok {
		return APIGWEnvelope{}, fmt.Errorf("missing required header %q", "X-Hub-Signature-256")
	}
	return env, nil
}

func (w *Worker) deleteMessage(ctx context.Context, receipt string) error {
	_, err := w.Client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(w.QueueURL),
		ReceiptHandle: aws.String(receipt),
	})
	return err
}

func (w *Worker) vOrDefault(v, def int32) int32 {
	if v <= 0 {
		return def
	}
	return v
}
