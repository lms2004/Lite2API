package gateway

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/lms2004/lite2api/internal/config"
)

type completionStreamCall struct {
	id, name, kind, callID           string
	args                             []byte
	initial                          []byte
	deltas, done                     bool
	started, argumentsDone, itemDone bool
}
type completionStreamChoice struct {
	payload, finished bool
	legacy            bool
	calls             map[int]*completionStreamCall
}
type completionToolStream struct {
	choices                  map[int]*completionStreamChoice
	native                   map[int]*completionStreamCall
	argumentBytes, callCount int
	tools                    int
	ids                      map[string]struct{}
	stopReason               string
}

func (c *responseCompletion) streamCall(group map[int]*completionStreamCall, index int) (*completionStreamCall, error) {
	if index < 0 || index >= maxCompletionTools {
		return nil, errors.New("upstream tool index exceeds the supported range")
	}
	call := group[index]
	if call == nil {
		if c.tools.callCount >= maxCompletionTools {
			return nil, errors.New("upstream returned too many tool calls")
		}
		call = &completionStreamCall{}
		group[index] = call
		c.tools.callCount++
	}
	return call, nil
}
func (c *responseCompletion) appendArguments(call *completionStreamCall, part string) error {
	if call.done || call.argumentsDone {
		return errors.New("upstream appended arguments after tool completion")
	}
	if c.tools.argumentBytes+len(part) > maxCompletionArguments {
		return errors.New("upstream tool arguments exceed the inspection limit")
	}
	if !call.deltas {
		call.args = nil
		call.deltas = true
	}
	call.args = append(call.args, part...)
	c.tools.argumentBytes += len(part)
	return nil
}
func appendCallIdentity(dst *string, part string) error {
	if len(*dst)+len(part) > 256 {
		return errors.New("upstream tool identity exceeds 256 bytes")
	}
	*dst += part
	return nil
}
func (c *responseCompletion) validStreamCall(call *completionStreamCall, legacy bool) bool {
	return (legacy || call.id != "") && call.name != "" && c.requirements.acceptsTool(call.name) && validToolArguments(string(call.args))
}

func (c *responseCompletion) observeStreamContract(kind string, payload []byte) error {
	switch c.operation {
	case config.OperationOpenAIChat:
		return c.observeChatToolStream(payload)
	case config.OperationOpenAIResponses:
		return c.observeResponsesToolStream(kind, payload)
	case config.OperationAnthropic:
		return c.observeMessagesToolStream(kind, payload)
	}
	return nil
}

func (c *responseCompletion) observeChatToolStream(payload []byte) error {
	var event struct {
		Choices []struct {
			Index  json.RawMessage `json:"index"`
			Finish *string         `json:"finish_reason"`
			Delta  struct {
				Content json.RawMessage `json:"content"`
				Refusal json.RawMessage `json:"refusal"`
				Calls   []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
				Legacy *struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function_call"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return errors.New("invalid chat stream choice")
	}
	if c.tools.choices == nil {
		c.tools.choices = map[int]*completionStreamChoice{}
	}
	for _, choice := range event.Choices {
		index := 0
		if len(choice.Index) > 0 {
			if bytes.Equal(bytes.TrimSpace(choice.Index), []byte("null")) || json.Unmarshal(choice.Index, &index) != nil {
				return errors.New("upstream choice index must be an integer")
			}
		} else if c.requirements.ExpectedChoices > 1 {
			return errors.New("upstream stream choice is missing its index")
		}
		if index < 0 || index >= maxCompletionChoices || c.requirements.ExpectedChoices > 0 && index >= c.requirements.ExpectedChoices {
			return errors.New("upstream stream choice index does not match requested n")
		}
		current := c.tools.choices[index]
		if current == nil {
			current = &completionStreamChoice{calls: map[int]*completionStreamCall{}}
			c.tools.choices[index] = current
		}
		if current.finished {
			return errors.New("upstream changed a finished choice")
		}
		for _, raw := range []json.RawMessage{choice.Delta.Content, choice.Delta.Refusal} {
			if len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				var text string
				if json.Unmarshal(raw, &text) != nil {
					return errors.New("upstream text delta must be a string")
				}
				current.payload = true
			}
		}
		for _, delta := range choice.Delta.Calls {
			if current.legacy {
				return errors.New("upstream mixed legacy and modern function calls")
			}
			if delta.Index == nil {
				return errors.New("upstream tool delta has no index")
			}
			if c.requirements.MaxToolCalls > 0 && len(current.calls) >= c.requirements.MaxToolCalls && current.calls[*delta.Index] == nil {
				return errors.New("upstream ignored the single-tool-call requirement")
			}
			call, err := c.streamCall(current.calls, *delta.Index)
			if err != nil {
				return err
			}
			if c.requirements.ToolMode == "none" {
				return errors.New("upstream ignored tool_choice none")
			}
			if err := appendCallIdentity(&call.id, delta.ID); err != nil {
				return err
			}
			if err := appendCallIdentity(&call.name, delta.Function.Name); err != nil {
				return err
			}
			if delta.Type != "" {
				if call.kind != "" && call.kind != delta.Type {
					return errors.New("upstream changed tool type")
				}
				call.kind = delta.Type
			}
			if len(delta.Function.Arguments) > 0 {
				var part string
				if json.Unmarshal(delta.Function.Arguments, &part) != nil {
					return errors.New("upstream tool arguments delta must be a string")
				}
				if err := c.appendArguments(call, part); err != nil {
					return err
				}
			}
		}
		if choice.Delta.Legacy != nil {
			// Slot 127 is reserved only inside this legacy-only choice collection.
			if !current.legacy && len(current.calls) > 0 {
				return errors.New("upstream mixed legacy and modern function calls")
			}
			current.legacy = true
			call, err := c.streamCall(current.calls, 127)
			if err != nil {
				return err
			}
			call.kind = "legacy"
			if err := appendCallIdentity(&call.name, choice.Delta.Legacy.Name); err != nil {
				return err
			}
			if len(choice.Delta.Legacy.Arguments) > 0 {
				var part string
				if json.Unmarshal(choice.Delta.Legacy.Arguments, &part) != nil {
					return errors.New("upstream function arguments delta must be a string")
				}
				if err := c.appendArguments(call, part); err != nil {
					return err
				}
			}
		}
		if choice.Finish != nil && *choice.Finish != "" {
			tools := len(current.calls)
			if (c.requirements.ToolMode == "required" || *choice.Finish == "tool_calls" || *choice.Finish == "function_call") && tools == 0 {
				return errors.New("upstream did not return a required function call")
			}
			if tools > 0 {
				if *choice.Finish != "tool_calls" && *choice.Finish != "function_call" {
					return errors.New("upstream tool call has the wrong finish reason")
				}
				for _, call := range current.calls {
					legacy := call.kind == "legacy"
					if !legacy {
						if c.tools.ids == nil {
							c.tools.ids = map[string]struct{}{}
						}
						if _, exists := c.tools.ids[call.id]; exists {
							return errors.New("upstream repeated a tool call identity")
						}
						c.tools.ids[call.id] = struct{}{}
					}
					if !legacy && call.kind != "function" || !c.validStreamCall(call, legacy) {
						return errors.New("upstream function arguments or identity did not complete")
					}
				}
			} else if c.requirements.ExpectedChoices > 0 && !current.payload {
				return errors.New("upstream stream choice has no payload")
			}
			current.finished = true
		}
	}
	return nil
}

func (c *responseCompletion) completeChatStream() error {
	if c.requirements.ExpectedChoices > 0 && len(c.tools.choices) != c.requirements.ExpectedChoices {
		return errors.New("upstream returned fewer choices than requested n")
	}
	for _, choice := range c.tools.choices {
		if !choice.finished {
			return errIncompleteResponse
		}
	}
	return nil
}

func (c *responseCompletion) observeResponsesToolStream(kind string, payload []byte) error {
	var event struct {
		Index     *int            `json:"output_index"`
		ItemID    string          `json:"item_id"`
		Delta     json.RawMessage `json:"delta"`
		Arguments *string         `json:"arguments"`
		RawItem   json.RawMessage `json:"item"`
		Response  json.RawMessage `json:"response"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return errors.New("invalid Responses event")
	}
	if kind == "response.completed" {
		check := newResponseCompletion(config.OperationOpenAIResponses, c.path, "application/json", false, c.requirements)
		check.Feed(event.Response)
		if err := check.Finish(); err != nil {
			return err
		}
		var final struct {
			Output []struct {
				Type, ID, Name, Arguments string
				CallID                    string `json:"call_id"`
			} `json:"output"`
		}
		if json.Unmarshal(event.Response, &final) != nil {
			return errIncompleteResponse
		}
		for index, item := range final.Output {
			if item.Type == "function_call" {
				call := c.tools.native[index]
				if call == nil || !call.done {
					return errors.New("upstream omitted the streamed function-call completion events")
				}
			}
		}
		for index, call := range c.tools.native {
			if index >= len(final.Output) || !call.done {
				return errors.New("streamed tool did not complete in the final response")
			}
			item := final.Output[index]
			if item.Type != "function_call" || call.id != item.ID || call.name != item.Name || call.callID != item.CallID || !sameArguments(call.args, []byte(item.Arguments)) {
				return errors.New("completed tool disagrees with streamed identity or arguments")
			}
		}
		return nil
	}
	if kind != "response.output_item.added" && kind != "response.output_item.done" && kind != "response.function_call_arguments.delta" && kind != "response.function_call_arguments.done" {
		return nil
	}
	if event.Index == nil {
		return errors.New("upstream function event has no output index")
	}
	var item struct {
		Type, ID, Name, Arguments string
		CallID                    string `json:"call_id"`
	}
	if len(event.RawItem) > 0 {
		if json.Unmarshal(event.RawItem, &item) != nil {
			return errors.New("invalid output item")
		}
		if item.Type != "function_call" {
			return nil
		}
	}
	if c.tools.native == nil {
		c.tools.native = map[int]*completionStreamCall{}
	}
	if c.requirements.MaxToolCalls > 0 && len(c.tools.native) >= c.requirements.MaxToolCalls && c.tools.native[*event.Index] == nil {
		return errors.New("upstream ignored the single-tool-call requirement")
	}
	call, err := c.streamCall(c.tools.native, *event.Index)
	if err != nil {
		return err
	}
	if c.requirements.ToolMode == "none" {
		return errors.New("upstream ignored tool_choice none")
	}
	for _, identity := range []string{item.ID, event.ItemID, item.Name, item.CallID} {
		if len(identity) > 256 {
			return errors.New("upstream tool identity exceeds 256 bytes")
		}
	}
	for _, pair := range []struct {
		dst   *string
		value string
	}{{&call.id, item.ID}, {&call.id, event.ItemID}, {&call.name, item.Name}, {&call.callID, item.CallID}} {
		if pair.value != "" {
			if *pair.dst != "" && *pair.dst != pair.value {
				return errors.New("upstream changed streamed tool identity")
			}
			*pair.dst = pair.value
		}
	}
	if kind == "response.output_item.added" {
		if call.started || call.id == "" || call.callID == "" || !c.requirements.acceptsTool(call.name) {
			return errors.New("upstream tool item is repeated, incomplete or disallowed")
		}
		call.started = true
		return nil
	}
	if !call.started {
		return errors.New("upstream tool event precedes its item")
	}
	if kind == "response.function_call_arguments.delta" {
		var part string
		if json.Unmarshal(event.Delta, &part) != nil {
			return errors.New("upstream function delta must be a string")
		}
		return c.appendArguments(call, part)
	}
	arguments := item.Arguments
	if event.Arguments != nil {
		arguments = *event.Arguments
	}
	if !validToolArguments(arguments) || call.deltas && !sameArguments(call.args, []byte(arguments)) {
		return errors.New("upstream tool arguments did not complete consistently")
	}
	if kind == "response.function_call_arguments.done" {
		if call.argumentsDone || call.itemDone {
			return errors.New("upstream repeated or reordered function completion")
		}
		if !call.deltas {
			if err := c.appendArguments(call, arguments); err != nil {
				return err
			}
		}
		call.argumentsDone = true
		return nil
	}
	if !call.argumentsDone || call.itemDone || item.ID == "" || item.CallID == "" || item.Name == "" {
		return errors.New("upstream tool item completed without its arguments or identity")
	}
	call.itemDone = true
	call.done = true
	return nil
}

func sameArguments(a, b []byte) bool {
	var left, right bytes.Buffer
	if json.Compact(&left, a) != nil || json.Compact(&right, b) != nil {
		return false
	}
	return bytes.Equal(left.Bytes(), right.Bytes())
}

func (c *responseCompletion) observeMessagesToolStream(kind string, payload []byte) error {
	if kind != "content_block_start" && kind != "content_block_delta" && kind != "content_block_stop" && kind != "message_delta" && kind != "message_stop" {
		return nil
	}
	var event struct {
		Index *int `json:"index"`
		Block struct {
			Type, ID, Name string
			Input          json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type       string  `json:"type"`
			Partial    *string `json:"partial_json"`
			Text       *string `json:"text"`
			Thinking   *string `json:"thinking"`
			Signature  *string `json:"signature"`
			StopReason string  `json:"stop_reason"`
		} `json:"delta"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return errors.New("invalid Messages content event")
	}
	if kind == "message_delta" {
		if event.Delta.StopReason != "" {
			c.tools.stopReason = event.Delta.StopReason
		}
		return nil
	}
	if kind == "message_stop" {
		for _, call := range c.tools.native {
			if !call.done {
				return errors.New("upstream stopped before its content block completed")
			}
		}
		if c.requirements.ToolMode == "required" && c.tools.tools == 0 || c.tools.stopReason == "tool_use" && c.tools.tools == 0 || c.tools.tools > 0 && c.tools.stopReason != "tool_use" {
			return errors.New("upstream tool usage does not match the requested choice or stop reason")
		}
		return nil
	}
	if event.Index == nil {
		return errors.New("upstream content event has no index")
	}
	if c.tools.native == nil {
		c.tools.native = map[int]*completionStreamCall{}
	}
	if kind == "content_block_start" {
		if c.tools.native[*event.Index] != nil {
			return errors.New("upstream repeated a content block")
		}
		call, err := c.streamCall(c.tools.native, *event.Index)
		if err != nil {
			return err
		}
		if len(event.Block.ID) > 256 || len(event.Block.Name) > 256 {
			return errors.New("upstream tool identity exceeds 256 bytes")
		}
		call.kind = event.Block.Type
		call.id = event.Block.ID
		call.name = event.Block.Name
		if call.kind == "tool_use" {
			if c.requirements.MaxToolCalls > 0 && len(c.tools.ids) >= c.requirements.MaxToolCalls {
				return errors.New("upstream ignored the single-tool-call requirement")
			}
			if c.tools.ids == nil {
				c.tools.ids = map[string]struct{}{}
			}
			if _, exists := c.tools.ids[call.id]; exists {
				return errors.New("upstream repeated a tool call identity")
			}
			c.tools.ids[call.id] = struct{}{}
			if !c.requirements.acceptsTool(call.name) || call.id == "" || !validToolArguments(string(event.Block.Input)) {
				return errors.New("upstream tool_use is missing input or identity")
			}
			if c.tools.argumentBytes+len(event.Block.Input) > maxCompletionArguments {
				return errors.New("upstream tool arguments exceed the inspection limit")
			}
			c.tools.argumentBytes += len(event.Block.Input)
			call.initial = append([]byte(nil), event.Block.Input...)
			call.args = append([]byte(nil), event.Block.Input...)
		}
		return nil
	}
	call := c.tools.native[*event.Index]
	if call == nil || call.done {
		return errors.New("upstream changed an absent or completed content block")
	}
	if kind == "content_block_delta" {
		switch event.Delta.Type {
		case "input_json_delta":
			if call.kind != "tool_use" || event.Delta.Partial == nil {
				return errors.New("JSON delta requires a tool block and a string partial_json")
			}
			return c.appendArguments(call, *event.Delta.Partial)
		case "text_delta":
			if call.kind != "text" || event.Delta.Text == nil {
				return errors.New("text delta requires a text block and a string text")
			}
		case "thinking_delta":
			if call.kind != "thinking" || event.Delta.Thinking == nil {
				return errors.New("thinking delta requires a thinking block and a string thinking")
			}
		case "signature_delta":
			if call.kind != "thinking" || event.Delta.Signature == nil {
				return errors.New("signature delta requires a thinking block and a string signature")
			}
		}
	}
	if kind == "content_block_stop" {
		if call.kind == "tool_use" {
			if !c.validStreamCall(call, false) {
				return errors.New("upstream tool input did not complete")
			}
			if call.deltas && !bytes.Equal(bytes.TrimSpace(call.initial), []byte("{}")) && !sameArguments(call.initial, call.args) {
				return errors.New("upstream tool input disagrees with its initial input")
			}
			c.tools.tools++
		}
		call.done = true
	}
	return nil
}
