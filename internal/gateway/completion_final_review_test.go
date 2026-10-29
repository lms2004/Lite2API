package gateway

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestStreamDeliveryRejectsBufferedPartialTailAfterTerminal(t *testing.T) {
	for _, operation := range []string{config.OperationOpenAIChat, config.OperationOpenAIResponses, config.OperationAnthropic} {
		t.Run(operation, func(t *testing.T) {
			var body string
			switch operation {
			case config.OperationOpenAIChat:
				body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			case config.OperationOpenAIResponses:
				body = responsesDeltaFixture("response.output_text.delta", true)
			case config.OperationAnthropic:
				body = messagesDeltaReviewFixture("text", `{"type":"text_delta","text":"ok"}`)
			}
			for _, tail := range []string{"data: {\"error\":", "data: {\"error\":\"late\"}\n", "event: error\n"} {
				completion := newResponseCompletion(operation, "/v1/unused", "text/event-stream", true)
				response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body + tail))}
				downstream := httptest.NewRecorder()
				err := streamResponse(downstream, response, time.Second, completion)
				var contractError *responseCompletionError
				if !errors.As(err, &contractError) || downstream.Body.Len() != 0 {
					t.Fatalf("invalid terminal chunk was delivered: err=%v bytes=%d", err, downstream.Body.Len())
				}
			}
		})
	}
}

func messagesDeltaReviewFixture(blockType, delta string) string {
	return "data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"" + blockType + "\",\"text\":\"\",\"thinking\":\"\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":" + delta + "}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
}

func TestMessagesStreamDeltaMatchesBlockAndStringPayload(t *testing.T) {
	for _, delta := range []struct{ kind, field, block string }{
		{"text_delta", "text", "text"},
		{"thinking_delta", "thinking", "thinking"},
		{"signature_delta", "signature", "thinking"},
	} {
		for _, value := range []string{`"ok"`, `""`, `42`, `null`, `{}`, `[]`, `true`, ""} {
			t.Run(delta.kind+"/"+value, func(t *testing.T) {
				payload := `{"type":"` + delta.kind + `"`
				if value != "" {
					payload += `,"` + delta.field + `":` + value
				}
				payload += `}`
				completion := newResponseCompletion(config.OperationAnthropic, "/v1/messages", "text/event-stream", true)
				for _, b := range []byte(messagesDeltaReviewFixture(delta.block, payload)) {
					completion.Feed([]byte{b})
				}
				wantComplete := strings.HasPrefix(value, `"`)
				if err := completion.Finish(); (err == nil) != wantComplete {
					t.Fatalf("complete=%v err=%v", wantComplete, err)
				}
			})
		}
		t.Run(delta.kind+"/wrong_block", func(t *testing.T) {
			block := "text"
			if delta.block == block {
				block = "thinking"
			}
			completion := newResponseCompletion(config.OperationAnthropic, "/v1/messages", "text/event-stream", true)
			completion.Feed([]byte(messagesDeltaReviewFixture(block, `{"type":"`+delta.kind+`","`+delta.field+`":"ok"}`)))
			if completion.Finish() == nil {
				t.Fatal("delta for a different block type was accepted")
			}
		})
	}
}
