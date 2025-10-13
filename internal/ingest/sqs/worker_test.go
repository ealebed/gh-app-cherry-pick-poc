package sqs

import (
	"context"
	"encoding/json"
	"testing"
)

// ---- a tiny fake processor ----

type fakeHandler struct {
	lastEvent    string
	lastDelivery string
	lastPayload  []byte

	code int
	err  error
}

func (f *fakeHandler) HandleEvent(ctx context.Context, event, delivery string, payload []byte) (int, error) {
	f.lastEvent = event
	f.lastDelivery = delivery
	f.lastPayload = payload
	return f.code, f.err
}

// ---- tests ----

func Test_handleSQSMessage_MinimalPullRequest(t *testing.T) {
	w := &Worker{
		Processor: &fakeHandler{code: 200},
	}

	// Minimal GH pull_request payload (no headers).
	body := map[string]any{
		"action":       "closed",
		"pull_request": map[string]any{"merged": true},
		"installation": map[string]any{"id": 1},
		"repository": map[string]any{
			"name": "repo",
			"owner": map[string]any{
				"login": "owner",
			},
		},
		"number": 7,
	}
	raw, _ := json.Marshal(body)

	code, err := w.handleSQSMessage(context.Background(), raw, "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	fh := w.Processor.(*fakeHandler)
	if fh.lastEvent != "pull_request" {
		t.Fatalf("expected event=pull_request, got %q", fh.lastEvent)
	}
	if fh.lastDelivery == "" {
		t.Fatalf("expected non-empty delivery (fallback to message id)")
	}
	if string(fh.lastPayload) != string(raw) {
		t.Fatalf("payload mismatch")
	}
}

func Test_handleSQSMessage_BadEnvelope(t *testing.T) {
	w := &Worker{
		Processor: &fakeHandler{code: 200},
	}
	// Not JSON -> parser should fail
	code, err := w.handleSQSMessage(context.Background(), []byte("{{not json"), "m-2")
	if err == nil {
		t.Fatalf("expected error for bad envelope")
	}
	if code != 400 {
		t.Fatalf("expected 400 for bad envelope, got %d", code)
	}
}

func Test_handleSQSMessage_UnknownEvent_NoFailure(t *testing.T) {
	w := &Worker{
		// Make handler return 204 for unknown event.
		Processor: &fakeHandler{code: 204},
	}

	// An event the parser can detect but we treat as unknown in our handler,
	// e.g. a "ping" webhook shape (simplified).
	body := map[string]any{
		"zen": "Design for failure",
	}
	raw, _ := json.Marshal(body)

	// Note: our parser will likely classify this as "unknown" event.
	code, err := w.handleSQSMessage(context.Background(), raw, "m-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 204 {
		t.Fatalf("expected 204 passthrough, got %d", code)
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
