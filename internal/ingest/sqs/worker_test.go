package sqs

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// helper to build an SQS message with string attributes
func mkMessage(body string, attrs map[string]string) awstypes.Message {
	m := awstypes.Message{
		Body:          aws.String(body),
		MessageId:     aws.String("m-1"),
		ReceiptHandle: aws.String("r-1"),
	}
	if len(attrs) > 0 {
		m.MessageAttributes = make(map[string]awstypes.MessageAttributeValue, len(attrs))
		for k, v := range attrs {
			m.MessageAttributes[k] = awstypes.MessageAttributeValue{
				StringValue: aws.String(v),
				// Type is optional for our use; SQS sets it when publishing.
			}
		}
	}
	return m
}

func Test_toEnvelope_AlreadyWrappedEnvelope(t *testing.T) {
	w := &Worker{}

	orig := APIGWEnvelope{
		Headers: map[string]string{
			"X-GitHub-Event":      "pull_request",
			"X-Hub-Signature-256": "sha256=abc123",
			"X-GitHub-Delivery":   "d-1",
		},
		Body: `{"action":"opened"}`,
	}
	b, _ := json.Marshal(orig)

	msg := mkMessage(string(b), nil)

	got, err := w.toEnvelope(msg)
	if err != nil {
		t.Fatalf("toEnvelope error: %v", err)
	}
	// Expect headers/body preserved exactly
	if got.Body != orig.Body {
		t.Fatalf("body mismatch: got=%q want=%q", got.Body, orig.Body)
	}
	for k, v := range orig.Headers {
		if got.Headers[k] != v {
			t.Fatalf("header %s mismatch: got=%q want=%q", k, got.Headers[k], v)
		}
	}
}

func Test_toEnvelope_RawBodyWithAttributes(t *testing.T) {
	w := &Worker{}

	body := `{"action":"closed"}`
	msg := mkMessage(body, map[string]string{
		"X-GitHub-Event":      "pull_request",
		"X-Hub-Signature-256": "sha256=deadbeef",
		"X-GitHub-Delivery":   "delivery-123",
	})

	got, err := w.toEnvelope(msg)
	if err != nil {
		t.Fatalf("toEnvelope error: %v", err)
	}
	if got.Body != body {
		t.Fatalf("body mismatch: got=%q want=%q", got.Body, body)
	}
	if got.Headers["X-GitHub-Event"] != "pull_request" {
		t.Fatalf("event header missing or wrong: %q", got.Headers["X-GitHub-Event"])
	}
	if got.Headers["X-Hub-Signature-256"] != "sha256=deadbeef" {
		t.Fatalf("sig header missing or wrong: %q", got.Headers["X-Hub-Signature-256"])
	}
	if got.Headers["X-GitHub-Delivery"] != "delivery-123" {
		t.Fatalf("delivery header missing or wrong: %q", got.Headers["X-GitHub-Delivery"])
	}
}

func Test_toEnvelope_MissingRequiredHeaders(t *testing.T) {
	w := &Worker{}

	// Missing signature header
	msg := mkMessage(`{"action":"anything"}`, map[string]string{
		"X-GitHub-Event": "pull_request",
	})

	_, err := w.toEnvelope(msg)
	if err == nil {
		t.Fatalf("expected error for missing X-Hub-Signature-256, got nil")
	}

	// Missing event header
	msg = mkMessage(`{"action":"anything"}`, map[string]string{
		"X-Hub-Signature-256": "sha256=abc",
	})
	_, err = w.toEnvelope(msg)
	if err == nil {
		t.Fatalf("expected error for missing X-GitHub-Event, got nil")
	}
}

func Test_toEnvelope_EmptyBody(t *testing.T) {
	w := &Worker{}

	// Body is empty (nil pointer yields "")
	msg := awstypes.Message{
		Body:          nil,
		MessageId:     aws.String("m-2"),
		ReceiptHandle: aws.String("r-2"),
	}

	_, err := w.toEnvelope(msg)
	if err == nil {
		t.Fatalf("expected error for empty body, got nil")
	}
}

func Test_vOrDefault(t *testing.T) {
	w := &Worker{}

	if got := w.vOrDefault(0, 10); got != 10 {
		t.Fatalf("vOrDefault(0,10) = %d, want 10", got)
	}
	if got := w.vOrDefault(-5, 10); got != 10 {
		t.Fatalf("vOrDefault(-5,10) = %d, want 10", got)
	}
	if got := w.vOrDefault(7, 10); got != 7 {
		t.Fatalf("vOrDefault(7,10) = %d, want 7", got)
	}
}
