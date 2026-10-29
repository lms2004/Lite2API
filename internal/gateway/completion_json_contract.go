package gateway

import (
	"bytes"
	"errors"
	"strconv"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

type completionResultFrame struct {
	index                          int
	indexed                        bool
	name, id, callID, kind, finish string
	arguments, payload             bool
	tools, numbers                 int
}

func validToolArguments(value string) bool {
	raw := bytes.TrimSpace([]byte(value))
	if len(raw) == 0 || raw[0] != '{' || len(raw) > maxCompletionArguments {
		return false
	}
	check := jsonCompletion{}
	return check.feed(raw) == nil && check.finish() == nil
}

func (j *jsonCompletion) resultFrame(path string) *completionFrame {
	for n := len(j.stack) - 1; n >= 0; n-- {
		if j.stack[n].path == path {
			return &j.stack[n]
		}
	}
	return nil
}

func (j *jsonCompletion) observeResultValue(path, value string, kind byte) {
	if j.operation == "" || len(j.stack) == 0 {
		return
	}
	current := &j.stack[len(j.stack)-1]
	field := current.key
	identityFrame := current.path == "choices[].message.tool_calls[]" || current.path == "choices[].message.tool_calls[].function" || current.path == "choices[].message.function_call" || current.path == "output[]" || current.path == "content[]"
	if identityFrame && (field == "id" || field == "name" || field == "call_id") && kind == 's' && (j.overflow || len(value) > 256) {
		j.semanticErr = errors.New("upstream tool identity exceeds 256 bytes")
		return
	}
	switch field {
	case "index":
		n, err := strconv.Atoi(value)
		if kind == 'v' && err == nil && n >= 0 {
			current.result.index = n
			current.result.indexed = true
		} else if current.path == "choices[]" {
			j.semanticErr = errors.New("upstream choice index must be a nonnegative integer")
			return
		}
	case "type":
		if kind == 's' {
			current.result.kind = value
		}
	case "name":
		if kind == 's' {
			current.result.name = value
		}
	case "id":
		if kind == 's' {
			current.result.id = value
		}
	case "call_id":
		if kind == 's' {
			current.result.callID = value
		}
	case "finish_reason":
		if kind == 's' {
			current.result.finish = value
		}
	}
	switch path {
	case "choices[].message.content", "choices[].message.refusal":
		if kind == 's' {
			current.result.payload = true
		}
	case "choices[].message.tool_calls[].function.arguments", "choices[].message.function_call.arguments", "output[].arguments":
		if path == "output[].arguments" && j.operation != config.OperationOpenAIResponses || path != "output[].arguments" && j.operation != config.OperationOpenAIChat {
			return
		}
		if kind != 's' || !validToolArguments(value) {
			j.semanticErr = errors.New("upstream function arguments are not a complete JSON object")
			return
		}
		current.result.arguments = true
	case "content[].text", "content[].thinking", "content[].data", "output[].content[].text", "output[].content[].refusal":
		if kind == 's' {
			current.result.payload = true
		}
	case "output[].encrypted_content":
		if kind == 's' && value != "" {
			current.result.payload = true
		}
	case "results[].relevance_score", "results[].score":
		if j.operation == config.OperationRerank && kind == 'v' {
			if _, err := strconv.ParseFloat(value, 64); err == nil {
				current.result.payload = true
			}
		}
	case "data[].embedding[]":
		if j.operation != config.OperationEmbeddings {
			return
		}
		if kind != 'v' {
			j.semanticErr = errors.New("upstream embedding contains a non-numeric value")
			return
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			j.semanticErr = errors.New("upstream embedding contains an invalid number")
			return
		}
		current.result.numbers++
	case "data[]", "choices[]", "output[]", "content[]", "results[]":
		relevant := path == "data[]" && (j.operation == config.OperationEmbeddings || j.operation == config.OperationImages) || path == "choices[]" && j.operation == config.OperationOpenAIChat || path == "output[]" && j.operation == config.OperationOpenAIResponses || path == "content[]" && j.operation == config.OperationAnthropic || path == "results[]" && j.operation == config.OperationRerank
		if relevant && kind != '{' {
			j.semanticErr = errors.New("upstream result item must be an object")
		}
	}
	if j.operation == config.OperationImages && (path == "data[].url" || path == "data[].b64_json") && kind == 's' && value != "" {
		current.result.payload = true
	}
}

func (j *jsonCompletion) closeResultFrame(frame completionFrame) error {
	if j.operation == "" {
		return nil
	}
	r := frame.result
	switch frame.path {
	case "choices[].message.tool_calls[].function":
		parent := j.resultFrame("choices[].message.tool_calls[]")
		if parent != nil {
			parent.result.name = r.name
			parent.result.arguments = r.arguments
		}
	case "choices[].message.tool_calls[]", "choices[].message.function_call":
		legacy := frame.path == "choices[].message.function_call"
		if r.name == "" || !r.arguments || (!legacy && (r.id == "" || r.kind != "function")) || !j.requirements.acceptsTool(r.name) {
			return errors.New("upstream returned an incomplete or disallowed function call")
		}
		id := r.id
		if legacy {
			id = ""
		}
		if err := j.recordToolID(id); err != nil {
			return err
		}
		if parent := j.resultFrame("choices[].message"); parent != nil {
			parent.result.tools++
			parent.result.payload = true
		}
	case "choices[].message":
		if !r.payload {
			return errors.New("upstream message has no supported payload")
		}
		if parent := j.resultFrame("choices[]"); parent != nil {
			parent.result.tools = r.tools
			parent.result.payload = true
		}
	case "choices[]":
		if j.requirements.MaxToolCalls > 0 && r.tools > j.requirements.MaxToolCalls {
			return errors.New("upstream ignored the single-tool-call requirement")
		}
		if !r.payload {
			return errors.New("upstream choice has no message payload")
		}
		if j.requirements.ExpectedChoices > 0 {
			if !r.indexed && j.requirements.ExpectedChoices > 1 || r.index >= j.requirements.ExpectedChoices || j.seen[r.index] {
				return errors.New("upstream choice indexes do not match requested n")
			}
			j.seen[r.index] = true
		}
		if r.tools > 0 && r.finish != "tool_calls" && r.finish != "function_call" {
			return errors.New("upstream function call has no matching finish reason")
		}
		if (r.finish == "tool_calls" || r.finish == "function_call" || j.requirements.ToolMode == "required") && r.tools == 0 {
			return errors.New("upstream did not return the required function call")
		}
	case "output[].content[]":
		if j.operation == config.OperationOpenAIResponses {
			if !r.payload || (r.kind != "output_text" && r.kind != "refusal") {
				return errors.New("upstream response content has no text or refusal payload")
			}
			if parent := j.resultFrame("output[]"); parent != nil {
				parent.result.payload = true
			}
		}
	case "results[]":
		if j.operation == config.OperationRerank && (!r.indexed || !r.payload) {
			return errors.New("upstream rerank result has no index or score")
		}
	case "output[]":
		if j.operation != config.OperationOpenAIResponses {
			return nil
		}
		if r.kind == "function_call" {
			if r.name == "" || r.callID == "" || !r.arguments || !j.requirements.acceptsTool(r.name) {
				return errors.New("upstream returned an incomplete or disallowed function call")
			}
			if err := j.recordToolID(r.callID); err != nil {
				return err
			}
			j.tools++
		} else if r.kind == "message" {
			if !r.payload {
				return errors.New("upstream response message has no content array")
			}
		} else if r.kind == "" {
			return errors.New("upstream response item has no type")
		}
	case "content[]":
		if j.operation != config.OperationAnthropic {
			return nil
		}
		if r.kind == "tool_use" {
			if r.name == "" || r.id == "" || !r.arguments || !j.requirements.acceptsTool(r.name) {
				return errors.New("upstream returned an incomplete or disallowed tool_use")
			}
			if err := j.recordToolID(r.id); err != nil {
				return err
			}
			j.tools++
		} else if !r.payload {
			return errors.New("upstream content block has no supported payload")
		}
	case "data[].embedding":
		if j.operation == config.OperationEmbeddings {
			if frame.kind != '[' || r.numbers == 0 {
				return errors.New("upstream embedding is empty")
			}
			if parent := j.resultFrame("data[]"); parent != nil {
				parent.dataReady = true
			}
		}
	case "data[]":
		if j.operation == config.OperationEmbeddings && !frame.dataReady {
			return errors.New("upstream data item has no embedding")
		}
		if j.operation == config.OperationImages && !r.payload {
			return errors.New("upstream data item has no image payload")
		}
	}
	return nil
}

func (j *jsonCompletion) openResultFrame(path string, kind byte) {
	if j.operation == "" {
		return
	}
	if path == "content[].input" && kind == '{' && len(j.stack) > 1 {
		j.stack[len(j.stack)-2].result.arguments = true
	}

}

func completionStringLimit(path string, key bool) int {
	if !key && (strings.HasSuffix(path, ".function.arguments") || path == "choices[].message.function_call.arguments" || path == "output[].arguments") {
		return maxCompletionArguments
	}
	return 512
}

func (j *jsonCompletion) recordToolID(id string) error {
	j.toolCount++
	if j.toolCount > maxCompletionTools {
		return errors.New("upstream returned too many tool calls")
	}
	if id == "" {
		return nil
	}
	if j.toolIDs == nil {
		j.toolIDs = map[string]struct{}{}
	}
	if _, exists := j.toolIDs[id]; exists {
		return errors.New("upstream repeated a tool call identity")
	}
	j.toolIDs[id] = struct{}{}
	return nil
}
