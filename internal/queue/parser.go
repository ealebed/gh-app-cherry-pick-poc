// internal/queue/parser.go
package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// ErrUnknownEvent is returned when we cannot determine the GitHub event type
// from either an envelope's headers or the raw payload.
var ErrUnknownEvent = errors.New("unknown event")

// Envelope matches the API-Gateway-like shape we sometimes receive:
//
//	{
//	  "headers": {"X-GitHub-Event":"pull_request", ...},
//	  "body": "<raw GH JSON as string>"  OR  { ... GH JSON object ... }
//	}
type Envelope struct {
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// ParseSQSBody tries multiple formats and returns:
//
//	event    - GitHub event type (e.g., "pull_request", "create", "label")
//	delivery - delivery id if present ("" if not found)
//	payload  - the raw GitHub JSON payload (never an envelope)
//	err      - ErrUnknownEvent if we can’t detect the event; other errors for bad shapes
//
// Behavior:
// - If the message body is an Envelope with headers, we prefer headers.
// - If headers don’t include X-GitHub-Event, we try to infer from payload.
// - If body is raw GH JSON (no envelope), we infer from payload.
// - If we can’t infer, we return ErrUnknownEvent.
func ParseSQSBody(body []byte) (event, delivery string, payload []byte, err error) {
	b := bytes.TrimSpace(body)
	if len(b) == 0 {
		return "", "", nil, errors.New("empty message body")
	}

	// Try to parse as Envelope first.
	var env Envelope
	if json.Unmarshal(b, &env) == nil && (env.Headers != nil || len(env.Body) > 0) {
		// Extract payload from env.Body, which might be:
		//  - a JSON string containing the GH JSON
		//  - a JSON object that IS the GH payload
		var s string
		if len(env.Body) > 0 && json.Unmarshal(env.Body, &s) == nil {
			// body is a JSON string containing the GH JSON
			payload = []byte(s)
		} else {
			// body is likely the GH JSON object already
			payload = env.Body
		}

		// Prefer headers if available
		if env.Headers != nil {
			event = trim(env.Headers["X-GitHub-Event"])
			delivery = trim(env.Headers["X-GitHub-Delivery"])
		}

		// If event is still empty, infer from payload
		if event == "" {
			ev, derr := detectEventFromPayload(payload)
			if derr != nil {
				return "", "", nil, derr
			}
			event = ev
		}
		return event, delivery, payload, nil
	}

	// Not an Envelope: treat as raw GH JSON and try to detect the event.
	ev, derr := detectEventFromPayload(b)
	if derr != nil {
		return "", "", nil, derr
	}
	return ev, "", b, nil
}

// detectEventFromPayload looks at top-level fields of the GitHub webhook JSON
// to infer the event type. It’s intentionally conservative/tolerant.
func detectEventFromPayload(p []byte) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(p, &m); err != nil {
		return "", err
	}

	// pull_request events have a top-level "pull_request" object.
	if _, ok := m["pull_request"]; ok {
		return "pull_request", nil
	}

	// create events have "ref_type": "branch" (or "tag")
	if v, ok := m["ref_type"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return "create", nil
		}
	}

	// label events have "label" and an "action".
	if _, hasLabel := m["label"]; hasLabel {
		if _, hasAction := m["action"]; hasAction {
			return "label", nil
		}
	}

	return "", ErrUnknownEvent
}

func trim(s string) string { return strings.TrimSpace(s) }
