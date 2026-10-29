package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func sseContractFrame(kind string, payload any) string {
	raw, _ := json.Marshal(payload)
	event := ""
	if kind != "" {
		event = "event: " + kind + "\n"
	}
	return event + "data: " + string(raw) + "\n\n"
}

func nativeFunctionStream(operation string) string {
	if operation == config.OperationOpenAIChat {
		return chatToolStream(`{"value":42}`)
	}
	if operation == config.OperationOpenAIResponses {
		item := map[string]any{"type": "function_call", "id": "item-1", "call_id": "call-1", "name": "lookup", "arguments": ""}
		body := sseContractFrame("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
		for _, part := range []string{`{"value":`, `42}`} {
			body += sseContractFrame("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "item-1", "delta": part})
		}
		body += sseContractFrame("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "item_id": "item-1", "arguments": `{"value":42}`})
		item["arguments"] = `{"value":42}`
		item["status"] = "completed"
		body += sseContractFrame("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		body += sseContractFrame("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "error": nil, "output": []any{item}}})
		return body
	}
	body := sseContractFrame("message_start", map[string]any{"type": "message_start", "message": map[string]any{"type": "message", "content": []any{}}})
	body += sseContractFrame("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call-1", "name": "lookup", "input": map[string]any{}}})
	for _, part := range []string{`{"value":`, `42}`} {
		body += sseContractFrame("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": part}})
	}
	body += sseContractFrame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	body += sseContractFrame("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}})
	body += sseContractFrame("message_stop", map[string]any{"type": "message_stop"})
	return body
}

func nativeTextStream(operation string) string {
	if operation == config.OperationOpenAIChat {
		return "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"roundtrip:42\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	}
	if operation == config.OperationOpenAIResponses {
		return sseContractFrame("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "roundtrip:42"}) + sseContractFrame("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "roundtrip:42"}}}}}})
	}
	return sseContractFrame("message_start", map[string]any{"type": "message_start", "message": map[string]any{"type": "message", "content": []any{}}}) +
		sseContractFrame("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}) +
		sseContractFrame("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "roundtrip:42"}}) +
		sseContractFrame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}) +
		sseContractFrame("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}) + sseContractFrame("message_stop", map[string]any{"type": "message_stop"})
}

func TestNativeStreamToolsRequireConsistentArguments(t *testing.T) {
	for _, operation := range []string{config.OperationOpenAIResponses, config.OperationAnthropic} {
		request := `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"required"}`
		if operation == config.OperationAnthropic {
			request = `{"tools":[{"name":"lookup"}],"tool_choice":{"type":"any"}}`
		}
		requirements := completionRequirementsForTest(t, operation, request)
		body := nativeFunctionStream(operation)
		cases := []struct {
			name, body string
			valid      bool
		}{{"complete", body, true}, {"broken JSON", strings.Replace(body, `42}`, `42`, 1), false}, {"no terminal", body[:strings.LastIndex(body, "event:")], false}}
		if operation == config.OperationOpenAIResponses {
			cases = append(cases, struct {
				name, body string
				valid      bool
			}{"contradictory final", strings.Replace(body, `\"value\":42}`, `\"value\":43}`, 1), false})
		}
		for _, tt := range cases {
			t.Run(operation+"/"+tt.name, func(t *testing.T) {
				c := newResponseCompletion(operation, "/unused", "text/event-stream", true, requirements)
				for _, b := range []byte(tt.body) {
					c.Feed([]byte{b})
				}
				if err := c.Finish(); (err == nil) != tt.valid {
					t.Fatalf("valid=%v err=%v", tt.valid, err)
				}
			})
		}
	}
}

func TestGatewayClientToolRoundTripAcrossNativeProtocols(t *testing.T) {
	for _, operation := range []string{config.OperationOpenAIChat, config.OperationOpenAIResponses, config.OperationAnthropic} {
		for _, stream := range []bool{false, true} {
			t.Run(operation+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				var calls atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					attempt := calls.Add(1)
					if attempt == 2 && (!strings.Contains(string(raw), "42") || !strings.Contains(string(raw), "call-1")) {
						t.Errorf("client tool result was not forwarded: %s", raw)
					}
					response := responseFunction
					if operation == config.OperationOpenAIResponses {
						response = `{"status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"{\"value\":42}"}]}`
					}
					if operation == config.OperationAnthropic {
						response = `{"content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{"value":42}}],"stop_reason":"tool_use"}`
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						response = nativeFunctionStream(operation)
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					if attempt == 2 {
						response = `{"choices":[{"index":0,"message":{"content":"roundtrip:42"},"finish_reason":"stop"}]}`
						if operation == config.OperationOpenAIResponses {
							response = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"roundtrip:42"}]}]}`
						}
						if operation == config.OperationAnthropic {
							response = `{"content":[{"type":"text","text":"roundtrip:42"}],"stop_reason":"end_turn"}`
						}
						if stream {
							response = nativeTextStream(operation)
						}
					}
					_, _ = io.WriteString(w, response)
				}))
				defer upstream.Close()
				accountType := "openai"
				path := "/v1/chat/completions"
				if operation == config.OperationOpenAIResponses {
					path = "/v1/responses"
				}
				if operation == config.OperationAnthropic {
					accountType = "anthropic"
					path = "/v1/messages"
				}
				g := newTestGateway(t, []config.Account{{ID: "native", Type: accountType, BaseURL: upstream.URL + "/v1", APIKey: "fixture", Models: []string{"m"}, Operations: []string{operation}, Enabled: true, Weight: 1}}, nil)
				server := httptest.NewServer(http.HandlerFunc(g.ServeGateway))
				defer server.Close()
				tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}
				choice := any("required")
				if operation == config.OperationOpenAIResponses {
					tools = []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}
				}
				if operation == config.OperationAnthropic {
					tools = []any{map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}}}
					choice = map[string]any{"type": "any"}
				}
				initial := map[string]any{"model": "m", "stream": stream, "tools": tools, "tool_choice": choice, "max_tokens": 128}
				if operation == config.OperationOpenAIResponses {
					initial["input"] = "run lookup"
				} else {
					initial["messages"] = []any{map[string]any{"role": "user", "content": "run lookup"}}
				}
				perform := func(body map[string]any) []byte {
					t.Helper()
					encoded, _ := json.Marshal(body)
					req, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(string(encoded)))
					req.Header.Set("Authorization", "Bearer gateway-secret")
					req.Header.Set("Content-Type", "application/json")
					resp, err := server.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					result, err := io.ReadAll(resp.Body)
					if err != nil || resp.StatusCode != http.StatusOK || strings.Contains(string(result), "incomplete_upstream_response") {
						t.Fatalf("status=%d read=%v response=%s", resp.StatusCode, err, result)
					}
					return result
				}
				first := perform(initial)
				id, name, args := extractClientFunction(t, operation, stream, first)
				if id != "call-1" || name != "lookup" {
					t.Fatalf("wrong client tool identity: %q/%q", id, name)
				}
				var input struct {
					Value int `json:"value"`
				}
				if json.Unmarshal([]byte(args), &input) != nil || input.Value != 42 {
					t.Fatalf("tool arguments did not survive delivery: %s", args)
				}
				// This is client-owned tool execution. The gateway forwards the result and
				// checks the next model response; it never executes lookup itself.
				toolResult := strconv.Itoa(input.Value)
				if operation == config.OperationOpenAIChat {
					initial["tool_choice"] = "auto"
					initial["messages"] = []any{map[string]any{"role": "user", "content": "run lookup"}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}, map[string]any{"role": "tool", "tool_call_id": id, "content": toolResult}}
				}
				if operation == config.OperationOpenAIResponses {
					initial["tool_choice"] = "auto"
					initial["input"] = []any{map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args}, map[string]any{"type": "function_call_output", "call_id": id, "output": toolResult}}
				}
				if operation == config.OperationAnthropic {
					initial["tool_choice"] = map[string]any{"type": "auto"}
					initial["messages"] = []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": toolResult}}}}
				}
				final := perform(initial)
				if !strings.Contains(string(final), "roundtrip:42") {
					t.Fatalf("tool return was not consumed: %s", final)
				}
				stats := g.Stats()
				if calls.Load() != 2 || stats.Requests != 2 || stats.Successful != 2 || stats.Failovers != 0 {
					t.Fatalf("calls=%d stats=%+v", calls.Load(), stats)
				}
			})
		}
	}
}

func extractClientFunction(t *testing.T, operation string, stream bool, body []byte) (string, string, string) {
	t.Helper()
	var id, name, args string
	payloads := [][]byte{body}
	if stream {
		payloads = nil
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "data: ") {
				raw := []byte(strings.TrimPrefix(line, "data: "))
				if json.Valid(raw) {
					payloads = append(payloads, raw)
				}
			}
		}
	}
	for _, raw := range payloads {
		if operation == config.OperationOpenAIChat {
			var event struct {
				Choices []struct {
					Message struct {
						Calls []struct {
							ID       string
							Function struct{ Name, Arguments string }
						} `json:"tool_calls"`
					}
					Delta struct {
						Calls []struct {
							ID       string
							Function struct{ Name, Arguments string }
						} `json:"tool_calls"`
					}
				}
			}
			_ = json.Unmarshal(raw, &event)
			for _, c := range event.Choices {
				for _, call := range c.Message.Calls {
					id, name, args = call.ID, call.Function.Name, call.Function.Arguments
				}
				for _, call := range c.Delta.Calls {
					id += call.ID
					name += call.Function.Name
					args += call.Function.Arguments
				}
			}
		}
		if operation == config.OperationOpenAIResponses {
			var envelope struct{ Response json.RawMessage }
			_ = json.Unmarshal(raw, &envelope)
			if len(envelope.Response) > 0 {
				raw = envelope.Response
			}
			var event struct {
				Output []struct {
					Type, Name, Arguments string
					CallID                string `json:"call_id"`
				}
			}
			_ = json.Unmarshal(raw, &event)
			for _, call := range event.Output {
				if call.Type == "function_call" {
					id, name, args = call.CallID, call.Name, call.Arguments
				}
			}
		}
		if operation == config.OperationAnthropic {
			var event struct {
				Content []struct {
					Type, ID, Name string
					Input          json.RawMessage
				}
				Block struct{ Type, ID, Name string } `json:"content_block"`
				Delta struct {
					Partial string `json:"partial_json"`
				}
			}
			_ = json.Unmarshal(raw, &event)
			for _, call := range event.Content {
				if call.Type == "tool_use" {
					id, name, args = call.ID, call.Name, string(call.Input)
				}
			}
			if event.Block.Type == "tool_use" {
				id, name = event.Block.ID, event.Block.Name
			}
			args += event.Delta.Partial
		}
	}
	if id == "" || name == "" || args == "" {
		t.Fatal(fmt.Sprintf("client received no executable tool: operation=%s stream=%v", operation, stream))
	}
	return id, name, args
}

func TestGatewayRequestedResultMismatchFailsWithoutReplay(t *testing.T) {
	cases := []struct {
		name, path, request, response string
		stream                        bool
		status, calls                 int
	}{
		{"missing requested choice", "/v1/chat/completions", `{"model":"m","n":2}`, completedChat, false, http.StatusBadGateway, 1},
		{"forced function got text", "/v1/chat/completions", requestFunction, completedChat, false, http.StatusBadGateway, 1},
		{"forced streamed function got text", "/v1/chat/completions", strings.TrimSuffix(requestFunction, "}") + `,"stream":true}`, nativeTextStream(config.OperationOpenAIChat), true, http.StatusOK, 1},
		{"background unsupported", "/v1/responses", `{"model":"m","background":true}`, `{"status":"queued","output":[]}`, false, http.StatusBadRequest, 0},
		{"malformed tool choice", "/v1/chat/completions", `{"model":"m","tool_choice":{"type":"function"}}`, completedChat, false, http.StatusBadRequest, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = io.WriteString(w, tt.response)
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{{ID: "contract", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "fixture", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
			server := httptest.NewServer(http.HandlerFunc(g.ServeGateway))
			defer server.Close()
			req, _ := http.NewRequest(http.MethodPost, server.URL+tt.path, strings.NewReader(tt.request))
			req.Header.Set("Authorization", "Bearer gateway-secret")
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			stats := g.Stats()
			if resp.StatusCode != tt.status || calls.Load() != int64(tt.calls) || stats.Successful != 0 || stats.Failovers != 0 {
				t.Fatalf("status=%d calls=%d stats=%+v", resp.StatusCode, calls.Load(), stats)
			}
			if tt.stream && !strings.Contains(string(raw), "incomplete_upstream_response") {
				t.Fatalf("stream failed without a client error: %s", raw)
			}
			if tt.calls > 0 && (len(stats.Recent) != 1 || stats.Recent[0].Outcome != "incomplete_response" || stats.Recent[0].AttemptCount != 1) {
				t.Fatalf("logical result is not traceable: %+v", stats.Recent)
			}
		})
	}
}
