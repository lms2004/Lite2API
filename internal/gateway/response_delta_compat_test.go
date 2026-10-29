package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

// CLIProxy's Gemini/Antigravity Responses translator emits string deltas for
// text, reasoning and tool arguments. Anthropic message_delta uses an object.
// Shared envelope decoding must not impose one protocol's delta type on another.
func TestResponsesSSEStringDeltasKeepCompletionContract(t *testing.T) {
	for _, kind := range []string{"response.output_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta"} {
		for _, completed := range []bool{true, false} {
			name := kind
			if !completed {
				name += "/missing_completed"
			}
			t.Run(name, func(t *testing.T) {
				body := responsesDeltaFixture(kind, completed)
				c := newResponseCompletion(config.OperationOpenAIResponses, "/v1/responses", "text/event-stream", true)
				for _, b := range []byte(body) {
					c.Feed([]byte{b})
				}
				if err := c.Finish(); (err == nil) != completed {
					t.Fatalf("complete=%v error=%v", completed, err)
				}
			})
		}
	}
}

func responsesDeltaFixture(kind string, completed bool) string {
	frame := func(event, payload string) string { return "event: " + event + "\ndata: " + payload + "\n\n" }
	body := frame("response.created", `{"type":"response.created","response":{"id":"resp-fixture","status":"in_progress","output":[]}}`)
	if kind == "response.function_call_arguments.delta" {
		stream := nativeFunctionStream(config.OperationOpenAIResponses)
		if !completed {
			stream = stream[:strings.LastIndex(stream, "event: response.completed")]
		}
		return body + stream
	}
	body += frame(kind, `{"type":"`+kind+`","output_index":0,"delta":"fixture"}`)
	if completed {
		body += frame("response.completed", `{"type":"response.completed","response":{"id":"resp-fixture","status":"completed","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"fixture"}]}],"error":null}}`)
	}
	return body
}

func TestGatewayResponsesStringDeltaCompletesAndDoesNotReplay(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesDeltaFixture("response.output_text.delta", true))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "responses", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "fixture", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"fixture"}`))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	stats := g.Stats()
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "response.completed") || strings.Contains(w.Body.String(), "incomplete_upstream_response") || stats.Successful != 1 || stats.Failovers != 0 || len(stats.Recent) != 1 || stats.Recent[0].Outcome != "success" {
		t.Fatalf("status=%d stats=%+v", w.Code, stats)
	}
}

func TestMessagesSSERejectsStringDelta(t *testing.T) {
	c := newResponseCompletion(config.OperationAnthropic, "/v1/messages", "text/event-stream", true)
	c.Feed([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"content\":[]}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":\"invalid\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	if err := c.Finish(); err == nil {
		t.Fatal("Messages string delta was accepted")
	}
}
