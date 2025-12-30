package queue

import (
	"encoding/json"
	"errors"
	"testing"
)

func Test_trim(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no whitespace",
			input: "hello",
			want:  "hello",
		},
		{
			name:  "leading whitespace",
			input: "  hello",
			want:  "hello",
		},
		{
			name:  "trailing whitespace",
			input: "hello  ",
			want:  "hello",
		},
		{
			name:  "both sides",
			input: "  hello  ",
			want:  "hello",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trim(tt.input)
			if got != tt.want {
				t.Errorf("trim(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func Test_detectEventFromPayload(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantEvent string
		wantErr   bool
	}{
		{
			name:      "pull_request event",
			payload:   `{"pull_request": {"number": 123}}`,
			wantEvent: "pull_request",
			wantErr:   false,
		},
		{
			name:      "create event with branch",
			payload:   `{"ref_type": "branch", "ref": "devops-release/0021"}`,
			wantEvent: "create",
			wantErr:   false,
		},
		{
			name:      "create event with tag",
			payload:   `{"ref_type": "tag", "ref": "v1.0.0"}`,
			wantEvent: "create",
			wantErr:   false,
		},
		{
			name:      "label event",
			payload:   `{"label": {"name": "bug"}, "action": "created"}`,
			wantEvent: "label",
			wantErr:   false,
		},
		{
			name:      "unknown event",
			payload:   `{"something": "else"}`,
			wantEvent: "",
			wantErr:   true,
		},
		{
			name:      "invalid JSON",
			payload:   `{invalid json}`,
			wantEvent: "",
			wantErr:   true,
		},
		{
			name:      "empty payload",
			payload:   `{}`,
			wantEvent: "",
			wantErr:   true,
		},
		{
			name:      "create event with empty ref_type",
			payload:   `{"ref_type": ""}`,
			wantEvent: "",
			wantErr:   true,
		},
		{
			name:      "label event without action",
			payload:   `{"label": {"name": "bug"}}`,
			wantEvent: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectEventFromPayload([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Errorf("detectEventFromPayload() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got != tt.wantEvent {
					t.Errorf("detectEventFromPayload() = %q, want %q", got, tt.wantEvent)
				}
			} else {
				if !errors.Is(err, ErrUnknownEvent) && err == nil {
					t.Errorf("detectEventFromPayload() = %q, want error", got)
				}
			}
		})
	}
}

func TestParseSQSBody(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantEvent    string
		wantDelivery string
		wantErr      bool
	}{
		{
			name:         "envelope with headers",
			body:         `{"headers": {"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "delivery-123"}, "body": "{\"action\":\"opened\"}"}`,
			wantEvent:    "pull_request",
			wantDelivery: "delivery-123",
			wantErr:      false,
		},
		{
			name:         "envelope with body as object",
			body:         `{"headers": {"X-GitHub-Event": "create"}, "body": {"ref_type": "branch"}}`,
			wantEvent:    "create",
			wantDelivery: "",
			wantErr:      false,
		},
		{
			name:         "envelope without headers, infers from payload",
			body:         `{"body": "{\"pull_request\":{\"number\":123}}"}`,
			wantEvent:    "pull_request",
			wantDelivery: "",
			wantErr:      false,
		},
		{
			name:         "raw GitHub JSON",
			body:         `{"pull_request": {"number": 123}}`,
			wantEvent:    "pull_request",
			wantDelivery: "",
			wantErr:      false,
		},
		{
			name:         "raw GitHub JSON create event",
			body:         `{"ref_type": "branch", "ref": "devops-release/0021"}`,
			wantEvent:    "create",
			wantDelivery: "",
			wantErr:      false,
		},
		{
			name:         "empty body",
			body:         ``,
			wantEvent:    "",
			wantDelivery: "",
			wantErr:      true,
		},
		{
			name:         "whitespace only",
			body:         `   `,
			wantEvent:    "",
			wantDelivery: "",
			wantErr:      true,
		},
		{
			name:         "invalid JSON",
			body:         `{invalid}`,
			wantEvent:    "",
			wantDelivery: "",
			wantErr:      true,
		},
		{
			name:         "envelope with unknown event in payload",
			body:         `{"body": "{\"unknown\":\"event\"}"}`,
			wantEvent:    "",
			wantDelivery: "",
			wantErr:      true,
		},
		{
			name:         "envelope with trimmed headers",
			body:         `{"headers": {"X-GitHub-Event": "  pull_request  ", "X-GitHub-Delivery": "  delivery-123  "}, "body": "{}"}`,
			wantEvent:    "pull_request",
			wantDelivery: "delivery-123",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, delivery, payload, err := ParseSQSBody([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSQSBody() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if event != tt.wantEvent {
					t.Errorf("ParseSQSBody() event = %q, want %q", event, tt.wantEvent)
				}
				if delivery != tt.wantDelivery {
					t.Errorf("ParseSQSBody() delivery = %q, want %q", delivery, tt.wantDelivery)
				}
				if len(payload) == 0 {
					t.Errorf("ParseSQSBody() payload is empty")
				}
			} else {
				if !errors.Is(err, ErrUnknownEvent) && err == nil {
					t.Errorf("ParseSQSBody() should return error for invalid input")
				}
			}
		})
	}
}

func TestParseSQSBody_EnvelopeBodyAsString(t *testing.T) {
	// Test that envelope body can be a JSON string containing the GitHub payload
	body := `{"headers": {"X-GitHub-Event": "pull_request"}, "body": "{\"action\":\"opened\",\"pull_request\":{\"number\":123}}"}`

	event, _, payload, err := ParseSQSBody([]byte(body))
	if err != nil {
		t.Fatalf("ParseSQSBody() error = %v", err)
	}
	if event != "pull_request" {
		t.Errorf("ParseSQSBody() event = %q, want %q", event, "pull_request")
	}
	if len(payload) == 0 {
		t.Errorf("ParseSQSBody() payload is empty")
	}

	// Verify payload is valid JSON
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Errorf("ParseSQSBody() payload is not valid JSON: %v", err)
	}
}
