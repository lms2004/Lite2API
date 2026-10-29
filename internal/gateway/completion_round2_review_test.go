package gateway

import (
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestRound2CompletionReviewRejectsInvalidSingleChoiceIndex(t *testing.T) {
	for _, index := range []string{`-1`, `null`, `"zero"`, `0.5`} {
		t.Run(index, func(t *testing.T) {
			r := completionRequirementsForTest(t, config.OperationOpenAIChat, `{"n":1}`)
			c := newResponseCompletion(config.OperationOpenAIChat, "/unused", "application/json", false, r)
			c.Feed([]byte(`{"choices":[{"index":` + index + `,"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			if c.Finish() == nil {
				t.Fatal("invalid explicitly supplied choice index was accepted as zero")
			}
		})
	}
}

func twoFunctionCalls(secondID string) string {
	return `{"choices":[{"index":0,"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}},{"id":"` + secondID + `","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
}

func TestRound2CompletionReviewHonorsSerialToolsAndUniqueIdentities(t *testing.T) {
	for _, tt := range []struct{ name, request, body string }{
		{"parallel disabled", strings.TrimSuffix(requestFunction, "}") + `,"parallel_tool_calls":false}`, twoFunctionCalls("call-2")},
		{"duplicate call identity", requestFunction, twoFunctionCalls("call-1")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := completionRequirementsForTest(t, config.OperationOpenAIChat, tt.request)
			c := newResponseCompletion(config.OperationOpenAIChat, "/unused", "application/json", false, r)
			c.Feed([]byte(tt.body))
			if c.Finish() == nil {
				t.Fatal("returned tool calls violate the client's invocation contract")
			}
		})
	}
}

func TestRound2CompletionReviewResponsesToolLifecycleAndIdentity(t *testing.T) {
	valid := nativeFunctionStream(config.OperationOpenAIResponses)
	lastID := strings.LastIndex(valid, `"call_id":"call-1"`)
	changedFinalCall := valid[:lastID] + strings.Replace(valid[lastID:], `"call_id":"call-1"`, `"call_id":"changed-call"`, 1)
	itemDone := strings.Index(valid, "event: response.output_item.done\n")
	changedItem := valid[:itemDone] + strings.ReplaceAll(valid[itemDone:], `"id":"item-1"`, `"id":"changed-item"`)
	var missingDone strings.Builder
	for _, frame := range strings.Split(valid, "\n\n") {
		if frame == "" || strings.Contains(frame, "event: response.function_call_arguments.done\n") || strings.Contains(frame, "event: response.output_item.done\n") {
			continue
		}
		missingDone.WriteString(frame + "\n\n")
	}
	for _, tt := range []struct{ name, body string }{
		{"changed final call id", changedFinalCall},
		{"changed item identity at done", changedItem},
		{"missing tool done events", missingDone.String()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := completionRequirementsForTest(t, config.OperationOpenAIResponses, `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"required"}`)
			c := newResponseCompletion(config.OperationOpenAIResponses, "/unused", "text/event-stream", true, r)
			c.Feed([]byte(tt.body))
			if c.Finish() == nil {
				t.Fatal("conflicting or incomplete streamed tool lifecycle was accepted")
			}
		})
	}
}
