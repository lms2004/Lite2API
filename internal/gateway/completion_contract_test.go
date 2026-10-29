package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func completionRequirementsForTest(t *testing.T, operation, body string) CompletionRequirements {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	r, err := requestCompletionRequirements(envelope, operation)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const requestFunction = `{"model":"m","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"}`
const responseFunction = `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"value\":42}"}}]},"finish_reason":"tool_calls"}]}`

func TestRequestedCompletionContracts(t *testing.T) {
	cases := []struct {
		name, operation, request, response string
		valid                              bool
	}{
		{"invalid explicit index", config.OperationOpenAIChat, `{}`, `{"choices":[{"index":null,"message":{"content":"a"},"finish_reason":"stop"}]}`, false},
		{"n missing choice", config.OperationOpenAIChat, `{"n":2}`, completedChat, false},
		{"n duplicate index", config.OperationOpenAIChat, `{"n":2}`, `{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":"stop"},{"index":0,"message":{"content":"b"},"finish_reason":"stop"}]}`, false},
		{"n complete", config.OperationOpenAIChat, `{"n":2}`, `{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":"stop"},{"index":1,"message":{"content":"b"},"finish_reason":"stop"}]}`, true},
		{"required got text", config.OperationOpenAIChat, requestFunction, completedChat, false},
		{"required function", config.OperationOpenAIChat, requestFunction, responseFunction, true},
		{"auto text", config.OperationOpenAIChat, strings.Replace(requestFunction, `"required"`, `"auto"`, 1), completedChat, true},
		{"none function", config.OperationOpenAIChat, strings.Replace(requestFunction, `"required"`, `"none"`, 1), responseFunction, false},
		{"wrong function", config.OperationOpenAIChat, requestFunction, strings.Replace(responseFunction, `"lookup"`, `"other"`, 1), false},
		{"incomplete arguments", config.OperationOpenAIChat, requestFunction, strings.Replace(responseFunction, `{\"value\":42}`, `{\"value\":`, 1), false},
		{"missing call ID", config.OperationOpenAIChat, requestFunction, strings.Replace(responseFunction, `"id":"call-1",`, ``, 1), false},
		{"missing payload", config.OperationOpenAIChat, `{}`, `{"choices":[{"message":{},"finish_reason":"stop"}]}`, false},
		{"duplicate type", config.OperationOpenAIChat, requestFunction, strings.Replace(responseFunction, `"type":"function"`, `"type":"function","type":"function"`, 1), false},
		{"responses required got text", config.OperationOpenAIResponses, `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"required"}`, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}]}`, false},
		{"responses function", config.OperationOpenAIResponses, `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":{"type":"function","name":"lookup"}}`, `{"status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"{}"}]}`, true},
		{"messages required got text", config.OperationAnthropic, `{"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`, `{"content":[{"type":"text","text":"a"}],"stop_reason":"end_turn"}`, false},
		{"messages function", config.OperationAnthropic, `{"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"lookup"}}`, `{"content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{}}],"stop_reason":"tool_use"}`, true},
		{"messages bad input", config.OperationAnthropic, `{"tools":[{"name":"lookup"}],"tool_choice":{"type":"any"}}`, `{"content":[{"type":"tool_use","id":"call-1","name":"lookup","input":"{}"}],"stop_reason":"tool_use"}`, false},
		{"responses hollow message", config.OperationOpenAIResponses, `{}`, `{"status":"completed","output":[{"type":"message","content":[{}]}]}`, false},
		{"rerank hollow result", config.OperationRerank, `{}`, `{"results":[{}]}`, false},
		{"rerank complete", config.OperationRerank, `{}`, `{"results":[{"index":0,"relevance_score":0}]}`, true},
		{"empty embedding", config.OperationEmbeddings, `{}`, `{"data":[{"embedding":[]}]}`, false},
		{"mixed broken embedding", config.OperationEmbeddings, `{}`, `{"data":[{"embedding":[0.1]},{}]}`, false},
		{"boolean embedding", config.OperationEmbeddings, `{}`, `{"data":[{"embedding":[true]}]}`, false},
		{"numeric embedding", config.OperationEmbeddings, `{}`, `{"data":[{"embedding":[-0.1,0.2]}]}`, true},
		{"mixed broken images", config.OperationImages, `{}`, `{"data":[{"url":"https://example.invalid/a"},{}]}`, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := completionRequirementsForTest(t, tt.operation, tt.request)
			c := newResponseCompletion(tt.operation, "/unused", "application/json", false, r)
			for _, b := range []byte(tt.response) {
				c.Feed([]byte{b})
			}
			if err := c.Finish(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}

func TestCompletionRequestRejectsUnsupportedPromises(t *testing.T) {
	for _, tt := range []struct{ operation, request string }{
		{config.OperationOpenAIResponses, `{"background":true}`},
		{config.OperationOpenAIResponses, `{"background":"true"}`},
		{config.OperationOpenAIChat, `{"n":0}`},
		{config.OperationOpenAIChat, `{"n":129}`},
		{config.OperationOpenAIChat, `{"n":1.5}`},
		{config.OperationOpenAIChat, `{"parallel_tool_calls":null}`},
		{config.OperationOpenAIChat, `{"parallel_tool_calls":"false"}`},
		{config.OperationAnthropic, `{"tools":[{"name":"lookup"}],"tool_choice":{"type":"any","disable_parallel_tool_use":null}}`},
		{config.OperationOpenAIChat, `{"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"function","function":{"name":"lookup","name":"other"}}}`},
		{config.OperationOpenAIChat, `{"tool_choice":"required"}`},
		{config.OperationOpenAIChat, `{"tool_choice":42}`},
		{config.OperationOpenAIChat, `{"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"function","function":{"name":"absent"}}}`},
		{config.OperationAnthropic, `{"tool_choice":"required"}`},
	} {
		t.Run(tt.request, func(t *testing.T) {
			var envelope map[string]json.RawMessage
			_ = json.Unmarshal([]byte(tt.request), &envelope)
			if _, err := requestCompletionRequirements(envelope, tt.operation); err == nil {
				t.Fatal("unsupported promise was accepted")
			}
		})
	}
}

func chatToolStream(arguments string) string {
	return "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":" + strconvJSON(arguments) + "}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
}
func strconvJSON(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }

func TestStreamRequestedToolsAndChoices(t *testing.T) {
	requirements := completionRequirementsForTest(t, config.OperationOpenAIChat, requestFunction)
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"complete tool", chatToolStream(`{"value":42}`), true},
		{"broken args", chatToolStream(`{"value":`), false},
		{"required plain text", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"text\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newResponseCompletion(config.OperationOpenAIChat, "/v1/chat/completions", "text/event-stream", true, requirements)
			for _, b := range []byte(tt.body) {
				c.Feed([]byte{b})
			}
			if err := c.Finish(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
	c := newResponseCompletion(config.OperationOpenAIChat, "/v1/chat/completions", "text/event-stream", true, completionRequirementsForTest(t, config.OperationOpenAIChat, `{"n":2}`))
	c.Feed([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	if err := c.Finish(); err == nil {
		t.Fatal("missing requested choice was accepted")
	}
}

func TestNativeOrdinarySerialToolRequirement(t *testing.T) {
	for _, operation := range []string{config.OperationOpenAIResponses, config.OperationAnthropic} {
		request := `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"required","parallel_tool_calls":false}`
		body := `{"status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"{}"},{"type":"function_call","id":"item-2","call_id":"call-2","name":"lookup","arguments":"{}"}]}`
		if operation == config.OperationAnthropic {
			request = `{"tools":[{"name":"lookup"}],"tool_choice":{"type":"any","disable_parallel_tool_use":true}}`
			body = `{"content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{}},{"type":"tool_use","id":"call-2","name":"lookup","input":{}}],"stop_reason":"tool_use"}`
		}
		t.Run(operation, func(t *testing.T) {
			r := completionRequirementsForTest(t, operation, request)
			c := newResponseCompletion(operation, "/unused", "application/json", false, r)
			c.Feed([]byte(body))
			if err := c.Finish(); err == nil {
				t.Fatal("multiple tools ignored the native serial requirement")
			}
		})
	}
}

func TestCompletionToolDefinitionMetadataIsBounded(t *testing.T) {
	large := `{"tools":[{"type":"function","function":{"name":"lookup","description":"` + strings.Repeat("x", 2<<20) + `","parameters":{"type":"object"}}}],"tool_choice":"required"}`
	r := completionRequirementsForTest(t, config.OperationOpenAIChat, large)
	if _, ok := r.ToolNames["lookup"]; !ok {
		t.Fatal("large schema metadata hid the selected function")
	}
	tooMany := `{"tools":[` + strings.Repeat(`{"type":"function","function":{"name":"lookup"}},`, maxCompletionTools) + `{"type":"function","function":{"name":"lookup"}}]}`
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal([]byte(tooMany), &envelope)
	if _, err := requestCompletionRequirements(envelope, config.OperationOpenAIChat); err == nil {
		t.Fatal("unbounded tool definitions were accepted")
	}
}
