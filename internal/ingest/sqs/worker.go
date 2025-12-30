package sqs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	qparser "github.com/ealebed/gh-app-cherry-pick-poc/internal/queue"
)

// Handler is implemented by the processor layer.
// Return an HTTP-like status and an error (nil on success).
type Handler interface {
	HandleEvent(ctx context.Context, event, delivery string, payload []byte) (int, error)
}

// Worker polls SQS, parses message envelopes, and dispatches to a Handler.
type Worker struct {
	Client            *awssqs.Client
	QueueURL          string
	MaxMessages       int32 // 1..10
	WaitTimeSeconds   int32 // 0..20
	VisibilityTimeout int32 // seconds
	DeleteOn4xx       bool

	Processor Handler
}

// Run starts a long-poll receive loop until ctx is canceled.
//
//nolint:gocyclo // Complex SQS polling loop with multiple error handling paths
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

		out, err := w.Client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(w.QueueURL),
			MaxNumberOfMessages: w.vOrDefault(w.MaxMessages, 10),
			WaitTimeSeconds:     w.vOrDefault(w.WaitTimeSeconds, 10),
			VisibilityTimeout:   w.vOrDefault(w.VisibilityTimeout, 120),
		})
		if err != nil {
			slog.Error("sqs.receive.error", "err", err)
			// small backoff to avoid a hot loop on persistent errors
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
			if m.ReceiptHandle == nil {
				slog.Warn("sqs.message.missing_receipt_handle", "messageID", aws.ToString(m.MessageId))
				continue
			}
			body := []byte(aws.ToString(m.Body))
			msgID := aws.ToString(m.MessageId)

			code, procErr := w.handleSQSMessage(ctx, body, msgID)

			// Decide deletion based on status and policy.
			shouldDelete := false
			switch {
			case code >= 200 && code < 300:
				shouldDelete = true
			case code >= 400 && code < 500:
				shouldDelete = w.DeleteOn4xx
			default:
				// 5xx/unknown -> keep for retry (visibility will expire)
			}

			if procErr != nil {
				slog.Warn("sqs.message.process_error",
					"status", code,
					"err", procErr,
					"delete", shouldDelete,
					"messageID", msgID,
				)
			} else {
				slog.Info("sqs.message.processed",
					"status", code,
					"delete", shouldDelete,
					"messageID", msgID,
				)
			}

			if shouldDelete {
				if derr := w.deleteMessage(ctx, aws.ToString(m.ReceiptHandle)); derr != nil {
					slog.Error("sqs.message.delete_error", "err", derr, "messageID", msgID)
				}
			}
		}
	}
}

// handleSQSMessage parses the envelope and dispatches to the Processor.
// It does not touch SQS; the caller controls deletion based on the return code.
func (w *Worker) handleSQSMessage(ctx context.Context, msgBody []byte, msgID string) (int, error) {
	event, delivery, payload, err := qparser.ParseSQSBody(msgBody)
	if err != nil {
		// Treat "unknown event" as a benign no-op (204, no error).
		if errors.Is(err, qparser.ErrUnknownEvent) {
			slog.Info("sqs.message.unknown_event", "messageID", msgID)
			return 204, nil
		}
		// All other parse/shape errors are bad envelopes (400) so deletion
		// policy can drop them to avoid poison loops.
		slog.Error("sqs.message.bad_envelope", "err", err, "messageID", msgID)
		return 400, err
	}
	if delivery == "" {
		delivery = msgID
	}
	if len(payload) == 0 {
		// Nothing useful to process.
		return 204, nil
	}

	// Dispatch to the processor.
	code, perr := w.Processor.HandleEvent(ctx, event, delivery, payload)
	if code == 0 {
		// Defensive default: success when processor forgot to set code.
		if perr == nil {
			code = 200
		} else {
			code = 500
		}
	}
	return code, perr
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
