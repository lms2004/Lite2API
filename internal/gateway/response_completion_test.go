package gateway

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

const completedChat = `{"id":"chat-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

func TestCompletionContractsIncremental(t *testing.T) {
	tests := []struct {
		name, operation, path, body string
		valid                       bool
	}{
		{"chat", config.OperationOpenAIChat, "/v1/chat/completions", completedChat, true},
		{"chat tool call", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{"tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, true},
		{"arbitrary JSON", config.OperationOpenAIChat, "/v1/chat/completions", `{"ok":true}`, false},
		{"unfinished choice", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{"content":"partial"},"finish_reason":null}]}`, false},
		{"missing one choice", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{},"finish_reason":"stop"},{"finish_reason":null}]}`, false},
		{"responses", config.OperationOpenAIResponses, "/v1/responses", `{"status":"completed","output":[],"error":null}`, true},
		{"responses pending", config.OperationOpenAIResponses, "/v1/responses", `{"status":"in_progress","output":[]}`, false},
		{"responses incomplete", config.OperationOpenAIResponses, "/v1/responses", `{"status":"incomplete","output":[]}`, false},
		{"anthropic", config.OperationAnthropic, "/v1/messages", `{"content":[],"stop_reason":"end_turn"}`, true},
		{"anthropic unfinished", config.OperationAnthropic, "/v1/messages", `{"content":[],"stop_reason":null}`, false},
		{"count tokens", config.OperationAnthropic, "/v1/messages/count_tokens", `{"input_tokens":0}`, true},
		{"invalid count", config.OperationAnthropic, "/v1/messages/count_tokens", `{"input_tokens":-1}`, false},
		{"embeddings", config.OperationEmbeddings, "/v1/embeddings", `{"data":[{"embedding":[0.1,0.2]}]}`, true},
		{"images", config.OperationImages, "/v1/images/generations", `{"data":[{"url":"https://example.invalid/image"}]}`, true},
		{"rerank empty result", config.OperationRerank, "/v1/rerank", `{"results":[]}`, true},
		{"truncated", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{},"finish_reason":"stop"}]`, false},
		{"extra JSON", config.OperationOpenAIChat, "/v1/chat/completions", completedChat + `{}`, false},
		{"error with success HTTP", config.OperationOpenAIChat, "/v1/chat/completions", `{"error":{"message":"private"},"choices":[{"message":{},"finish_reason":"stop"}]}`, false},
		{"trailing comma", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{},"finish_reason":"stop"}],}`, false},
		{"bad separator", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{},"finish_reason":"stop"}] "x":1}`, false},
		{"invalid escape", config.OperationOpenAIChat, "/v1/chat/completions", `{"choices":[{"message":{},"finish_reason":"stop"}],"x":"\q"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newResponseCompletion(tt.operation, tt.path, "application/json", false)
			for _, b := range []byte(tt.body) {
				c.Feed([]byte{b})
			}
			if err := c.Finish(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}

func TestLargeJSONCompletionUsesBoundedMemory(t *testing.T) {
	c := newResponseCompletion(config.OperationOpenAIChat, "/v1/chat/completions", "application/json", false)
	c.Feed([]byte(`{"choices":[{"message":{"content":"`))
	block := []byte(strings.Repeat("escaped\\ntext", 2048))
	for n := 0; n < 128; n++ {
		c.Feed(block)
	}
	c.Feed([]byte(`"},"finish_reason":"stop"}]}`))
	if err := c.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(c.json.token) > 512 || cap(c.line) > maxCompletionEventBytes {
		t.Fatalf("unbounded capture: %d", len(c.json.token))
	}
}

func TestSSECompletionContracts(t *testing.T) {
	tests := []struct {
		name, operation, body string
		valid                 bool
	}{
		{"chat done", config.OperationOpenAIChat, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true},
		{"chat missing done", config.OperationOpenAIChat, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\n", false},
		{"chat bare done", config.OperationOpenAIChat, "data: [DONE]\n\n", false},
		{"chat partial choice", config.OperationOpenAIChat, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"},{\"index\":1}]}\n\ndata: [DONE]\n\n", false},
		{"unterminated frame", config.OperationOpenAIChat, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n", false},
		{"error frame", config.OperationOpenAIChat, "data: {\"error\":{\"message\":\"private\"}}\n\n", false},
		{"responses", config.OperationOpenAIResponses, "event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\r\n\r\n", true},
		{"responses incomplete", config.OperationOpenAIResponses, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", false},
		{"anthropic", config.OperationAnthropic, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"content\":[]}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true},
		{"anthropic error", config.OperationAnthropic, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"private\"}}\n\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newResponseCompletion(tt.operation, "/unused", "text/event-stream", true)
			for _, b := range []byte(tt.body) {
				c.Feed([]byte{b})
			}
			if err := c.Finish(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}

func TestGatewayRejectsIncompleteResponseWithoutReplay(t *testing.T) {
	for _, body := range []string{"<html>login</html>", `{"choices":[{"message":{"content":"partial"}}]}`, `{"error":{"message":"private-token-canary"}}`} {
		t.Run(body, func(t *testing.T) {
			var firstCalls, secondCalls atomic.Int64
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { firstCalls.Add(1); _, _ = io.WriteString(w, body) }))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				_, _ = io.WriteString(w, completedChat)
			}))
			defer second.Close()
			accounts := []config.Account{{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}, {ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}
			g := newTestGateway(t, accounts, map[string]config.Route{"m": {Accounts: []string{"first", "second"}, Strategy: "priority"}})
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"m"}`))
			if w.Code != http.StatusBadGateway || firstCalls.Load() != 1 || secondCalls.Load() != 0 || strings.Contains(w.Body.String(), "private-token-canary") {
				t.Fatalf("status=%d calls=%d/%d body=%s", w.Code, firstCalls.Load(), secondCalls.Load(), w.Body.String())
			}
			stats := g.Stats()
			if stats.Successful != 0 || stats.Failed != 1 || stats.Failovers != 0 || stats.Recent[0].Outcome != "incomplete_response" {
				t.Fatalf("stats=%+v", stats)
			}
		})
	}
}

func TestGatewayReportsSSEFailureToClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","stream":true}`))
	if !strings.Contains(w.Body.String(), "partial") || !strings.Contains(w.Body.String(), "incomplete_upstream_response") || g.Stats().Successful != 0 || g.Stats().Recent[0].Outcome != "incomplete_response" {
		t.Fatalf("body=%s stats=%+v", w.Body.String(), g.Stats())
	}
}

func TestGatewayLargeJSONFailureAbortsTransport(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "truncated"}[complete], func(t *testing.T) {
			body := `{"choices":[{"message":{"content":"` + strings.Repeat("x", 128<<10)
			if complete {
				body += `"},"finish_reason":"stop"}]}`
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, _ = io.WriteString(w, body)
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
			server := httptest.NewServer(http.HandlerFunc(g.ServeGateway))
			defer server.Close()
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			req.Header.Set("Authorization", "Bearer gateway-secret")
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, readErr := io.ReadAll(resp.Body)
			if complete {
				if readErr != nil || !json.Valid(got) || g.Stats().Successful != 1 {
					t.Fatalf("read=%v valid=%v stats=%+v", readErr, json.Valid(got), g.Stats())
				}
			} else if readErr == nil || g.Stats().Successful != 0 {
				t.Fatalf("truncation reported success: err=%v stats=%+v", readErr, g.Stats())
			}
		})
	}
}

func TestStreamTerminalCompletesWithoutTransportEOF(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeGateway(httptest.NewRecorder(), gatewayRequest(`{"model":"m","stream":true}`))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal stream waited for connection close")
	}
	if g.Stats().Successful != 1 {
		t.Fatalf("stats=%+v", g.Stats())
	}
}

func TestGatewayNegotiatesDecodableUpstreamCompression(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("upstream negotiation=%q", got)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		compressed := gzip.NewWriter(w)
		_, _ = io.WriteString(compressed, completedChat)
		_ = compressed.Close()
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	req := gatewayRequest(`{"model":"m"}`)
	req.Header.Set("Accept-Encoding", "br, gzip")
	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" || w.Body.String() != completedChat || g.Stats().Successful != 1 {
		t.Fatalf("status=%d headers=%v body=%s stats=%+v", w.Code, w.Header(), w.Body.String(), g.Stats())
	}
}

func TestLargeImageCompletionDoesNotBufferBase64(t *testing.T) {
	c := newResponseCompletion(config.OperationImages, "/v1/images/generations", "application/json", false)
	c.Feed([]byte(`{"data":[{"b64_json":"`))
	block := []byte(strings.Repeat("YWJj", 2048))
	for n := 0; n < 128; n++ {
		c.Feed(block)
	}
	c.Feed([]byte(`"}]}`))
	if err := c.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(c.json.token) > 512 {
		t.Fatalf("retained %d base64 bytes", len(c.json.token))
	}
}

func TestGatewayRejectsResponseModeMismatchBeforeHeaders(t *testing.T) {
	for _, requestedStream := range []bool{false, true} {
		t.Run(strconv.FormatBool(requestedStream), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requestedStream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, completedChat)
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"m","stream":`+strconv.FormatBool(requestedStream)+`}`))
			if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "invalid_upstream_response") || g.Stats().Successful != 0 {
				t.Fatalf("status=%d body=%s stats=%+v", w.Code, w.Body.String(), g.Stats())
			}
		})
	}
}
