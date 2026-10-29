package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// Completion checks are deliberately separate from metadata capture: a bounded
// head/tail sample is useful for usage, but cannot prove a response completed.
var errIncompleteResponse = errors.New("upstream response did not complete the requested operation")

const completionPreviewBytes = 64 << 10
const maxCompletionEventBytes = 1 << 20

type responseCompletion struct {
	operation, path  string
	stream           bool
	json             jsonCompletion
	line, data       []byte
	event            string
	terminal, failed bool
	started, stopped bool
	err              error
	committed        bool
	requirements     CompletionRequirements
	tools            completionToolStream
	stopReason       string
}

func newResponseCompletion(operation, path, contentType string, requestedStream bool, requirements ...CompletionRequirements) *responseCompletion {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	c := &responseCompletion{operation: operation, path: path, stream: strings.EqualFold(mediaType, "text/event-stream")}
	if requestedStream && !c.stream {
		c.err = errors.New("upstream did not return the requested event stream")
	}
	if !requestedStream && c.stream {
		c.err = errors.New("upstream returned an unsolicited event stream")
	}
	if len(requirements) > 0 {
		c.requirements = requirements[0]
	}
	c.json.requirements = c.requirements
	c.json.operation = operation
	c.json.observe = c.observeJSON
	return c
}

func (c *responseCompletion) observeJSON(path, value string, kind byte) {
	switch path {
	case "error":
		if kind != 'n' {
			c.failed = true
		}
	case "status":
		if c.operation == config.OperationOpenAIResponses {
			c.terminal = kind == 's' && value == "completed"
			if value == "failed" || value == "incomplete" || value == "cancelled" {
				c.failed = true
			}
		}
	case "stop_reason":
		if c.operation == config.OperationAnthropic {
			c.terminal = kind == 's' && value != ""
			c.stopReason = value
		}
	}
}

func (c *responseCompletion) Feed(data []byte) {
	if c.err != nil {
		return
	}
	if !c.stream {
		c.err = c.json.feed(data)
		return
	}
	for _, b := range data {
		if b == '\n' {
			c.sseLine(bytes.TrimSuffix(c.line, []byte{'\r'}))
			c.line = c.line[:0]
			if c.err != nil {
				return
			}
		} else {
			if len(c.line) >= maxCompletionEventBytes {
				c.err = errors.New("upstream event exceeds completion inspection limit")
				return
			}
			c.line = append(c.line, b)
		}
	}
}

func (c *responseCompletion) sseLine(line []byte) {
	if len(line) == 0 {
		c.sseEvent()
		c.data = c.data[:0]
		c.event = ""
		return
	}
	if line[0] == ':' {
		return
	}
	field, value, _ := bytes.Cut(line, []byte{':'})
	value = bytes.TrimPrefix(value, []byte{' '})
	switch string(field) {
	case "event":
		c.event = string(value)
	case "data":
		if len(c.data)+len(value)+1 > maxCompletionEventBytes {
			c.err = errors.New("upstream event exceeds completion inspection limit")
			return
		}
		if len(c.data) > 0 {
			c.data = append(c.data, '\n')
		}
		c.data = append(c.data, value...)
	}
}

func (c *responseCompletion) sseEvent() {
	if len(c.data) == 0 {
		return
	}
	if c.terminal {
		c.err = errors.New("upstream sent data after its terminal event")
		return
	}
	if bytes.Equal(c.data, []byte("[DONE]")) {
		if c.operation != config.OperationOpenAIChat || c.json.choices == 0 || c.json.finishedChoices != c.json.choices {
			c.err = errIncompleteResponse
			return
		}
		if err := c.completeChatStream(); err != nil {
			c.err = err
			return
		}
		c.terminal = true
		return
	}
	syntax := jsonCompletion{}
	if err := syntax.feed(c.data); err != nil {
		c.err = err
		return
	}
	if err := syntax.finish(); err != nil {
		c.err = err
		return
	}
	var frame struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Index        int     `json:"index"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Response struct {
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
			Error  json.RawMessage `json:"error"`
		} `json:"response"`
		Message struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Delta json.RawMessage `json:"delta"`
	}
	if json.Unmarshal(c.data, &frame) != nil {
		c.err = errors.New("upstream returned an invalid JSON event")
		return
	}
	kind := frame.Type
	if kind == "" {
		kind = c.event
	}
	if c.operation != config.OperationOpenAIChat && c.event != "" && frame.Type != "" && c.event != frame.Type {
		c.err = errors.New("upstream event name conflicts with its type")
		return
	}
	if kind == "error" || len(frame.Error) > 0 && !bytes.Equal(frame.Error, []byte("null")) || kind == "response.failed" || kind == "response.incomplete" {
		c.failed = true
		c.err = errors.New("upstream returned a failure event")
		return
	}
	if err := c.observeStreamContract(kind, c.data); err != nil {
		c.err = err
		return
	}
	switch c.operation {
	case config.OperationOpenAIChat:
		// Use distinct choice indexes; usage-only chunks do not imply completion.
		for _, choice := range frame.Choices {
			if choice.Index < 0 || choice.Index >= 128 {
				c.err = errors.New("upstream choice index exceeds completion inspection limit")
				return
			}
			if !c.json.seen[choice.Index] {
				c.json.seen[choice.Index] = true
				c.json.choices++
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" && !c.json.finished[choice.Index] {
				c.json.finished[choice.Index] = true
				c.json.finishedChoices++
			}
		}
	case config.OperationOpenAIResponses:
		if kind == "response.completed" {
			if frame.Response.Status == "completed" && isJSONArray(frame.Response.Output) && (len(frame.Response.Error) == 0 || bytes.Equal(bytes.TrimSpace(frame.Response.Error), []byte("null"))) {
				c.terminal = true
			} else {
				c.err = errIncompleteResponse
			}
		}
	case config.OperationAnthropic:
		if kind == "message_start" && frame.Message.Type == "message" && isJSONArray(frame.Message.Content) {
			c.started = true
		}
		if kind == "message_delta" {
			// Responses delta events carry strings; only Messages uses this
			// object. Decode after selecting the protocol and event type.
			var delta struct {
				StopReason *string `json:"stop_reason"`
			}
			if json.Unmarshal(frame.Delta, &delta) != nil {
				c.err = errors.New("upstream returned an invalid message_delta object")
				return
			}
			if delta.StopReason != nil && *delta.StopReason != "" {
				c.stopped = true
			}
		}
		if kind == "message_stop" {
			if c.started && c.stopped {
				c.terminal = true
			} else {
				c.err = errIncompleteResponse
			}
		}
	default:
		c.err = errors.New("event streams are unsupported for this operation")
	}
}

func (c *responseCompletion) Finish() error {
	if c.err != nil {
		return c.err
	}
	if c.stream {
		// An SSE event is dispatched only by an empty line, not by transport EOF.
		if len(bytes.TrimSpace(c.line)) > 0 || len(c.data) > 0 || c.event != "" {
			return errIncompleteResponse
		}
		if !c.terminal || c.failed {
			return errIncompleteResponse
		}
		return nil
	}
	if err := c.json.finish(); err != nil {
		return err
	}
	if c.failed {
		return errors.New("upstream returned a failure in a successful HTTP response")
	}
	switch c.operation {
	case config.OperationOpenAIChat:
		if c.json.choices == 0 || c.json.finishedChoices != c.json.choices || c.requirements.ExpectedChoices > 0 && c.json.choices != c.requirements.ExpectedChoices {
			return errIncompleteResponse
		}
	case config.OperationOpenAIResponses:
		if !c.terminal || !c.json.output {
			return errIncompleteResponse
		}
	case config.OperationAnthropic:
		if strings.HasSuffix(c.path, "/count_tokens") {
			if !c.json.tokenCount {
				return errIncompleteResponse
			}
			return nil
		} else if !c.terminal || !c.json.content {
			return errIncompleteResponse
		}
	case config.OperationEmbeddings, config.OperationImages:
		if c.json.dataItems == 0 || c.operation == config.OperationImages && c.requirements.ExpectedChoices > 0 && c.json.dataItems != c.requirements.ExpectedChoices {
			return errIncompleteResponse
		}
	case config.OperationRerank:
		if !c.json.results {
			return errIncompleteResponse
		}
	default:
		return errors.New("operation has no response completion contract")
	}
	if c.requirements.MaxToolCalls > 0 && c.operation != config.OperationOpenAIChat && c.json.tools > c.requirements.MaxToolCalls {
		return errors.New("upstream ignored the single-tool-call requirement")
	}
	if c.operation == config.OperationAnthropic && (c.json.tools > 0 && c.stopReason != "tool_use" || c.stopReason == "tool_use" && c.json.tools == 0) {
		return errors.New("upstream tool_use has no matching stop reason")
	}
	if c.requirements.ToolMode == "required" && c.operation != config.OperationOpenAIChat && c.json.tools == 0 {
		return errors.New("upstream did not return the required function call")
	}
	return nil
}

func validateBufferedCompletion(operation, path string, body []byte) error {
	requirements := CompletionRequirements{}
	if operation == config.OperationOpenAIChat {
		// Diagnostic prompts are single-call probes. Keep the same choice
		// contract as a normal request so a provider cannot return multiple
		// choices and still be reported as a completed check.
		requirements.ExpectedChoices = 1
	}
	c := newResponseCompletion(operation, path, "application/json", false, requirements)
	c.Feed(body)
	return c.Finish()
}

func (c *responseCompletion) writeError(w http.ResponseWriter) {
	if !c.committed {
		writeProtocolError(w, c.operation, http.StatusBadGateway, "upstream returned an invalid or incomplete response", "upstream_error", "invalid_upstream_response")
		return
	}
	if c.stream {
		payload := map[string]any{"error": map[string]any{"type": "upstream_error", "code": "incomplete_upstream_response", "message": "upstream response failed before completion"}}
		event := ""
		if c.operation == config.OperationAnthropic {
			payload["type"] = "error"
			event = "event: error\n"
		}
		if c.operation == config.OperationOpenAIResponses {
			payload["type"] = "error"
			event = "event: error\n"
		}
		data, _ := json.Marshal(payload)
		_ = writeDownstreamChunk(w, append(append([]byte(event+"data: "), data...), '\n', '\n'), 0)
	}
}

// jsonCompletion validates JSON incrementally while retaining only short keys,
// terminal scalar values and a bounded nesting stack. Large text/base64 values
// are scanned, never buffered. This avoids both head/tail false positives and
// unbounded json.Decoder allocations for ordinary image or long text responses.
type jsonCompletion struct {
	operation                            string
	requirements                         CompletionRequirements
	semanticErr                          error
	tools                                int
	toolCount                            int
	toolIDs                              map[string]struct{}
	stack                                []completionFrame
	mode                                 byte
	token                                []byte
	overflow                             bool
	escape                               bool
	unicode                              int
	key                                  bool
	scalarPath                           string
	root, done                           bool
	observe                              func(string, string, byte)
	choices, finishedChoices, dataItems  int
	seen, finished                       [128]bool
	output, content, results, tokenCount bool
}
type completionFrame struct {
	kind, state        byte
	path, key          string
	finished           bool
	message, dataReady bool
	fields             uint64
	result             completionResultFrame
}

func (j *jsonCompletion) feed(data []byte) error {
	for _, b := range data {
		if err := j.step(b); err != nil {
			return err
		}
		if j.semanticErr != nil {
			return j.semanticErr
		}
	}
	return nil
}
func (j *jsonCompletion) appendToken(b byte) {
	if len(j.token) < completionStringLimit(j.scalarPath, j.key) {
		j.token = append(j.token, b)
	} else {
		j.overflow = true
	}
}
func (j *jsonCompletion) step(b byte) error {
	if j.mode == 's' {
		if j.unicode > 0 {
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
				return errors.New("invalid JSON string escape")
			}
			j.unicode--
			j.appendToken(b)
			return nil
		}
		if j.escape {
			j.escape = false
			if b == 'u' {
				j.unicode = 4
			} else if !bytes.ContainsRune([]byte(`"\/bfnrt`), rune(b)) {
				return errors.New("invalid JSON string escape")
			}
			j.appendToken(b)
			return nil
		}
		if b == '\\' {
			j.escape = true
			j.appendToken(b)
			return nil
		}
		if b == '"' {
			j.mode = 0
			value := ""
			if j.overflow && !j.key {
				value = "\x00"
			}
			if !j.overflow {
				raw := append(append([]byte{'"'}, j.token...), '"')
				if json.Unmarshal(raw, &value) != nil {
					return errors.New("invalid JSON string")
				}
			}
			if j.key {
				f := &j.stack[len(j.stack)-1]
				bit := completionFieldBit(value)
				if bit != 0 && f.fields&bit != 0 {
					return errors.New("upstream repeated a completion field")
				}
				f.fields |= bit
				// Literal dots/brackets in provider extension keys must not
				// collide with the structural paths used for completion.
				if strings.ContainsAny(value, ".[]") {
					value = "/" + value
				}
				f.key = value
				f.state = ':'
			} else {
				j.observeValue(j.scalarPath, value, 's')
				j.valueDone()
			}
			return nil
		}
		if b < 0x20 {
			return errors.New("invalid JSON control character")
		}
		j.appendToken(b)
		return nil
	}
	if j.mode == 'v' {
		if b == ',' || b == '}' || b == ']' || b == ' ' || b == '\r' || b == '\n' || b == '\t' {
			if err := j.scalarDone(); err != nil {
				return err
			}
			return j.step(b)
		}
		j.appendToken(b)
		return nil
	}
	if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
		return nil
	}
	if j.done {
		return errors.New("multiple upstream JSON values")
	}
	if len(j.stack) > 0 {
		f := &j.stack[len(j.stack)-1]
		if f.state == ',' {
			if b == ',' {
				if f.kind == '{' {
					f.state = 'k'
				} else {
					f.state = 'v'
				}
				return nil
			}
			if b == '}' && f.kind == '{' || b == ']' && f.kind == '[' {
				return j.close()
			}
			return errors.New("invalid JSON separator")
		}
		if f.state == ':' {
			if b != ':' {
				return errors.New("invalid JSON object separator")
			}
			f.state = 'v'
			return nil
		}
		if f.state == 'k' || f.state == 'K' {
			if b == '}' && f.state == 'K' {
				return j.close()
			}
			if b != '"' {
				return errors.New("invalid JSON object key")
			}
			j.beginString(true, "")
			return nil
		}
		if f.state == 'V' && b == ']' {
			return j.close()
		}
	}
	path := ""
	if len(j.stack) > 0 {
		f := j.stack[len(j.stack)-1]
		if f.kind == '{' {
			if f.path != "" {
				path = f.path + "."
			}
			path += f.key
		} else {
			path = f.path + "[]"
		}
	}
	if b == '{' || b == '[' {
		if len(j.stack) >= 128 {
			return errors.New("upstream JSON nesting limit exceeded")
		}
		if !j.root {
			if b != '{' {
				return errors.New("upstream JSON must be an object")
			}
			j.root = true
		}
		state := byte('K')
		if b == '[' {
			state = 'V'
		}
		j.stack = append(j.stack, completionFrame{kind: b, state: state, path: path})
		j.openResultFrame(path, b)
		if path == "choices[]" && b == '{' {
			j.choices++
		}
		if path == "choices[].message" && b == '{' && len(j.stack) > 1 {
			j.stack[len(j.stack)-2].message = true
		}

		if path == "output" && b == '[' {
			j.output = true
		}
		if path == "content" && b == '[' {
			j.content = true
		}
		if path == "results" && b == '[' {
			j.results = true
		}
		j.observeValue(path, "", b)
		return nil
	}
	if !j.root {
		return errors.New("upstream JSON must be an object")
	}
	if b == '"' {
		j.beginString(false, path)
		return nil
	}
	if b == '-' || b >= '0' && b <= '9' || b == 't' || b == 'f' || b == 'n' {
		j.mode = 'v'
		j.token = j.token[:0]
		j.overflow = false
		j.scalarPath = path
		j.appendToken(b)
		return nil
	}
	return errors.New("invalid upstream JSON value")
}
func (j *jsonCompletion) beginString(key bool, path string) {
	j.mode = 's'
	j.key = key
	j.scalarPath = path
	j.token = j.token[:0]
	j.overflow = false
}
func (j *jsonCompletion) scalarDone() error {
	if j.overflow || !json.Valid(j.token) {
		return errors.New("invalid upstream JSON scalar")
	}
	kind := byte('v')
	if bytes.Equal(j.token, []byte("null")) {
		kind = 'n'
	}
	j.observeValue(j.scalarPath, string(j.token), kind)
	j.mode = 0
	j.valueDone()
	return nil
}
func (j *jsonCompletion) observeValue(path, value string, kind byte) {
	j.observeResultValue(path, value, kind)
	if j.observe != nil {
		j.observe(path, value, kind)
	}
	if path == "choices[].finish_reason" && len(j.stack) > 0 {
		j.stack[len(j.stack)-1].finished = kind == 's' && value != ""
	}
	if j.operation == config.OperationImages && (path == "data[].url" || path == "data[].b64_json") && kind == 's' && value != "" && len(j.stack) > 0 {
		j.stack[len(j.stack)-1].dataReady = true
	}
	if path == "input_tokens" && kind == 'v' {
		var n int64
		if json.Unmarshal([]byte(value), &n) == nil && n >= 0 {
			j.tokenCount = true
		}
	}
}
func (j *jsonCompletion) valueDone() {
	if len(j.stack) == 0 {
		j.done = true
	} else {
		j.stack[len(j.stack)-1].state = ','
	}
}
func (j *jsonCompletion) close() error {
	if len(j.stack) == 0 {
		return errors.New("invalid JSON close")
	}
	frame := j.stack[len(j.stack)-1]
	j.stack = j.stack[:len(j.stack)-1]
	if err := j.closeResultFrame(frame); err != nil {
		return err
	}
	if frame.path == "data[]" && frame.dataReady {
		j.dataItems++
	}
	if frame.path == "choices[]" && frame.finished && frame.message {
		j.finishedChoices++
	}
	j.valueDone()
	return nil
}
func (j *jsonCompletion) finish() error {
	if j.mode == 'v' {
		if err := j.scalarDone(); err != nil {
			return err
		}
	}
	if j.mode != 0 || !j.done || len(j.stack) > 0 {
		return fmt.Errorf("%w: truncated JSON", errIncompleteResponse)
	}
	return nil
}

type responseCompletionError struct{ err error }

func (e *responseCompletionError) Error() string {
	return "upstream completion check failed: " + e.err.Error()
}
func (e *responseCompletionError) Unwrap() error { return e.err }

func isJSONArray(raw []byte) bool { raw = bytes.TrimSpace(raw); return len(raw) > 0 && raw[0] == '[' }
func completionFieldBit(key string) uint64 {
	switch key {
	case "choices":
		return 1 << 0
	case "message":
		return 1 << 1
	case "finish_reason":
		return 1 << 2
	case "output":
		return 1 << 3
	case "status":
		return 1 << 4
	case "error":
		return 1 << 5
	case "content":
		return 1 << 6
	case "stop_reason":
		return 1 << 7
	case "data":
		return 1 << 8
	case "results":
		return 1 << 9
	case "input_tokens":
		return 1 << 10
	case "embedding":
		return 1 << 11
	case "url":
		return 1 << 12
	case "b64_json":
		return 1 << 13
	case "index":
		return 1 << 14
	case "type":
		return 1 << 15
	case "id":
		return 1 << 16
	case "name":
		return 1 << 17
	case "arguments":
		return 1 << 18
	case "function":
		return 1 << 19
	case "function_call":
		return 1 << 20
	case "tool_calls":
		return 1 << 21
	case "call_id":
		return 1 << 22
	case "input":
		return 1 << 23
	case "delta":
		return 1 << 24
	case "response":
		return 1 << 25
	case "stop_sequence":
		return 1 << 26
	case "content_block":
		return 1 << 27
	case "partial_json":
		return 1 << 28
	case "output_index":
		return 1 << 29
	case "item":
		return 1 << 30
	case "disable_parallel_tool_use":
		return 1 << 31
	case "parallel_tool_calls":
		return 1 << 32
	}
	return 0
}
