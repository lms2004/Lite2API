package gateway

import (
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

// Independent contract checks: field names must not impersonate structural
// paths, and completion evidence belongs to the requested protocol.
func TestCompletionReviewRejectsMissingOrContradictoryPayload(t *testing.T) {
	for _, tt := range []struct {
		name, operation, body string
	}{
		{"literal choice path", config.OperationOpenAIChat, `{"choices[]":{"finish_reason":"stop"}}`},
		{"duplicate choices", config.OperationOpenAIChat, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"choices":[]}`},
		{"missing message", config.OperationOpenAIChat, `{"choices":[{"finish_reason":"stop"}]}`},
		{"responses wrong terminal", config.OperationOpenAIResponses, `{"stop_reason":"end_turn","output":[]}`},
		{"anthropic wrong terminal", config.OperationAnthropic, `{"status":"completed","content":[]}`},
		{"replaced output", config.OperationOpenAIResponses, `{"status":"completed","output":[],"output":null}`},
		{"literal embedding path", config.OperationEmbeddings, `{"data[]":{}}`},
		{"literal image path", config.OperationImages, `{"data[]":{}}`},
		{"missing embedding payload", config.OperationEmbeddings, `{"data":[{}]}`},
		{"missing image payload", config.OperationImages, `{"data":[{}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateBufferedCompletion(tt.operation, "/v1/unused", []byte(tt.body)); err == nil {
				t.Fatal("response without the operation's actual result was accepted")
			}
		})
	}
}

func TestCompletionReviewStreamRequiresPayloadAndRequestedFormat(t *testing.T) {
	for _, tt := range []struct {
		name, operation, body string
		requestedStream       bool
	}{
		{"responses missing output", config.OperationOpenAIResponses, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"anthropic missing message", config.OperationAnthropic, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true},
		{"unexpected streaming format", config.OperationOpenAIChat, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newResponseCompletion(tt.operation, "/v1/unused", "text/event-stream", tt.requestedStream)
			c.Feed([]byte(tt.body))
			if err := c.Finish(); err == nil {
				t.Fatal("stream without the requested complete result was accepted")
			}
		})
	}
}
