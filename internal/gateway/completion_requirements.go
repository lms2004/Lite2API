package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

const maxCompletionChoices = 128
const maxCompletionTools = 128
const maxCompletionArguments = 1 << 20

// CompletionRequirements describes client promises checked against the actual
// result. It does not decide model quality or execute client-owned tools.
type CompletionRequirements struct {
	ExpectedChoices int
	ToolMode        string // auto, none, required
	ToolName        string
	ToolNames       map[string]struct{}
	MaxToolCalls    int // zero keeps the bounded default; one disables parallel calls
}

func requestCompletionRequirements(envelope map[string]json.RawMessage, operation string) (CompletionRequirements, error) {
	out := CompletionRequirements{ExpectedChoices: 1, ToolMode: "auto", ToolNames: map[string]struct{}{}}
	if raw, exists := envelope["background"]; exists && operation == config.OperationOpenAIResponses {
		var enabled bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &enabled) != nil {
			return out, errors.New("background must be a boolean")
		}
		if enabled {
			return out, errors.New("background responses are unsupported: this gateway delivers synchronous completed calls")
		}
	}
	if raw, exists := envelope["n"]; exists {
		if operation != config.OperationOpenAIChat && operation != config.OperationImages {
			return out, errors.New("n is unsupported for this operation")
		}
		if json.Unmarshal(raw, &out.ExpectedChoices) != nil || out.ExpectedChoices < 1 || out.ExpectedChoices > maxCompletionChoices {
			return out, fmt.Errorf("n must be an integer between 1 and %d", maxCompletionChoices)
		}
	}
	for _, field := range []string{"tools", "functions"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
			return out, fmt.Errorf("%s must be an array", field)
		}
		names, count, parseErr := completionToolNames(raw, operation, field)
		if parseErr != nil {
			return out, fmt.Errorf("%s: %w", field, parseErr)
		}
		if count > maxCompletionTools {
			return out, fmt.Errorf("%s exceeds %d entries", field, maxCompletionTools)
		}
		for _, name := range names {
			out.ToolNames[name] = struct{}{}
		}

	}
	if raw, exists := envelope["parallel_tool_calls"]; exists {
		if operation != config.OperationOpenAIChat && operation != config.OperationOpenAIResponses {
			return out, errors.New("parallel_tool_calls is unsupported on this interface")
		}
		var enabled bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &enabled) != nil {
			return out, errors.New("parallel_tool_calls must be a boolean")
		}
		if !enabled {
			out.MaxToolCalls = 1
		}
	}
	choiceSet := false
	for _, field := range []string{"tool_choice", "function_call"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if err := completionContractJSON(raw); err != nil {
			return out, fmt.Errorf("%s: %w", field, err)
		}
		mode, name, err := completionToolChoice(raw, operation, field)
		if err != nil {
			return out, err
		}
		if choiceSet && (mode != out.ToolMode || name != out.ToolName) {
			return out, errors.New("tool_choice conflicts with function_call")
		}
		out.ToolMode, out.ToolName = mode, name
		if operation == config.OperationAnthropic && field == "tool_choice" {
			var native struct {
				Disabled json.RawMessage `json:"disable_parallel_tool_use"`
			}
			if json.Unmarshal(raw, &native) != nil {
				return out, errors.New("invalid tool choice")
			}
			if len(native.Disabled) > 0 {
				var disabled bool
				if bytes.Equal(bytes.TrimSpace(native.Disabled), []byte("null")) || json.Unmarshal(native.Disabled, &disabled) != nil {
					return out, errors.New("disable_parallel_tool_use must be a boolean")
				}
				if disabled {
					out.MaxToolCalls = 1
				}
			}
		}
		choiceSet = true
	}
	if out.ToolMode == "required" {
		if len(out.ToolNames) == 0 {
			return out, errors.New("required tool choice needs a supported function tool")
		}
		if out.ToolName != "" {
			if _, ok := out.ToolNames[out.ToolName]; !ok {
				return out, errors.New("selected tool is not present in tools or functions")
			}
		}
	}
	return out, nil
}

func completionToolChoice(raw []byte, operation, field string) (string, string, error) {
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		if operation == config.OperationAnthropic {
			return "", "", errors.New("Messages tool_choice must be an object")
		}
		switch choice {
		case "auto", "none":
			return choice, "", nil
		case "required":
			if field == "tool_choice" {
				return "required", "", nil
			}
		}
		return "", "", errors.New("unsupported tool choice")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return "", "", errors.New("tool choice must be a supported string or object")
	}
	var object struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &object) != nil {
		return "", "", errors.New("invalid tool choice")
	}
	if operation == config.OperationAnthropic {
		switch object.Type {
		case "auto", "none":
			return object.Type, "", nil
		case "any":
			return "required", "", nil
		case "tool":
			if strings.TrimSpace(object.Name) != "" {
				return "required", object.Name, nil
			}
		}
	} else if field == "function_call" && strings.TrimSpace(object.Name) != "" {
		return "required", object.Name, nil
	} else if object.Type == "function" {
		name := object.Name
		if operation == config.OperationOpenAIChat {
			name = object.Function.Name
		}
		if strings.TrimSpace(name) != "" {
			return "required", name, nil
		}
	}
	return "", "", errors.New("unsupported or incomplete tool choice")
}

func (r CompletionRequirements) acceptsTool(name string) bool {
	if r.ToolMode == "none" || name == "" || r.ToolName != "" && r.ToolName != name {
		return false
	}
	if r.ToolNames != nil {
		_, ok := r.ToolNames[name]
		return ok
	}
	return true
}

func completionContractJSON(raw []byte) error {
	check := jsonCompletion{}
	if err := check.feed([]byte(`{"value":`)); err != nil {
		return err
	}
	if err := check.feed(raw); err != nil {
		return err
	}
	if err := check.feed([]byte(`}`)); err != nil {
		return err
	}
	return check.finish()
}

// Reuse the bounded syntax walker to extract names without decoding/copying
// client-sized JSON schemas or retaining the whole tools array a second time.
func completionToolNames(raw []byte, operation, field string) ([]string, int, error) {
	expected := "value[].name"
	if operation == config.OperationOpenAIChat && field == "tools" {
		expected = "value[].function.name"
	}
	names := make([]string, 0)
	count := 0
	var invalid error
	check := jsonCompletion{}
	check.observe = func(path, value string, kind byte) {
		if path == "value[]" {
			if kind != '{' {
				invalid = errors.New("tool must be an object")
				check.semanticErr = invalid
			} else {
				count++
				if count > maxCompletionTools {
					invalid = errors.New("tool definition count exceeds the inspection limit")
					check.semanticErr = invalid
					return
				}
			}
		}
		if path == expected && kind == 's' && value != "" {
			if check.overflow || len(value) > 256 {
				invalid = errors.New("tool name exceeds 256 bytes")
				check.semanticErr = invalid
			} else {
				names = append(names, value)
			}
		}
	}
	if err := check.feed([]byte(`{"value":`)); err != nil {
		return nil, count, err
	}
	if err := check.feed(raw); err != nil {
		return nil, count, err
	}
	if err := check.feed([]byte(`}`)); err != nil {
		return nil, count, err
	}
	if err := check.finish(); err != nil {
		return nil, count, err
	}
	return names, count, invalid
}
