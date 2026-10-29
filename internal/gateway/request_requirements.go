package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// RequestRequirements affects selection, unlike bounded observability-only
// modality summaries. The JSON scanner does not decode another prompt tree.
type RequestRequirements struct {
	Features        []string
	ReasoningEffort string
	Continuation    bool
}

// validateOperationRequest rejects fields whose meaning belongs to another
// protocol before any source is selected. Forwarding them to an upstream can
// turn a caller mistake into an avoidable provider request or a late 502.
func validateOperationRequest(envelope map[string]json.RawMessage, operation string) error {
	nonNull := func(field string) bool {
		raw, ok := envelope[field]
		return ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	if operation != config.OperationOpenAIResponses {
		if nonNull("previous_response_id") {
			return errors.New("previous_response_id is supported only on the Responses interface")
		}
		if nonNull("reasoning") {
			return errors.New("reasoning is supported only on the Responses interface")
		}
		if nonNull("background") {
			return errors.New("background is supported only on the Responses interface")
		}
	}
	if operation != config.OperationOpenAIChat && operation != config.OperationOpenAIResponses {
		if nonNull("parallel_tool_calls") {
			return errors.New("parallel_tool_calls is unsupported on this interface")
		}
	}
	switch operation {
	case config.OperationEmbeddings, config.OperationImages, config.OperationRerank:
		if raw, ok := envelope["stream"]; ok {
			var stream bool
			if json.Unmarshal(raw, &stream) != nil || stream {
				return errors.New("streaming is unsupported on this interface")
			}
		}
		for _, field := range []string{"tools", "functions", "tool_choice", "function_call", "parallel_tool_calls"} {
			if nonNull(field) {
				return fmt.Errorf("%s is unsupported on this interface", field)
			}
		}
		if nonNull("reasoning_effort") {
			return errors.New("reasoning_effort is unsupported on this interface")
		}
	}
	return nil
}

func requestRequirements(envelope map[string]json.RawMessage, operation string) (RequestRequirements, error) {
	var requirements RequestRequirements
	if raw, exists := envelope["previous_response_id"]; exists && operation == config.OperationOpenAIResponses {
		if len(raw) > maxSessionKeyBytes*6+2 {
			return requirements, errors.New("previous_response_id must not exceed 4096 bytes")
		}
		var previous string
		if json.Unmarshal(raw, &previous) != nil {
			return requirements, errors.New("previous_response_id must be a string or null")
		}
		if len(previous) > maxSessionKeyBytes {
			return requirements, errors.New("previous_response_id must not exceed 4096 bytes")
		}
		requirements.Continuation = previous != ""
	}
	if raw, exists := envelope["stream"]; exists {
		var stream bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &stream) != nil {
			return requirements, errors.New("stream must be a boolean")
		}
		if stream {
			requirements.Features = append(requirements.Features, "stream")
		}
	}
	for _, field := range []string{"tools", "functions"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
				return requirements, fmt.Errorf("%s must be an array", field)
			}
			if len(bytes.TrimSpace(raw[1:len(raw)-1])) == 0 {
				continue
			}
			if !slices.Contains(requirements.Features, "tools") {
				requirements.Features = append(requirements.Features, "tools")
			}
		}
	}
	for _, field := range []string{"tool_choice", "function_call"} {
		raw := bytes.TrimSpace(envelope[field])
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`"none"`)) && !slices.Contains(requirements.Features, "tools") {
			requirements.Features = append(requirements.Features, "tools")
		}
	}
	for _, field := range []string{"messages", "input", "contents"} {
		if containsImageInput(envelope[field]) {
			requirements.Features = append(requirements.Features, "image_input")
			break
		}
	}
	toolHistory := false
	for _, field := range []string{"messages", "input", "contents"} {
		if containsRequestMarker(envelope[field], true) {
			toolHistory = true
			break
		}
	}
	if toolHistory && !slices.Contains(requirements.Features, "tools") {
		requirements.Features = append(requirements.Features, "tools")
	} else if !toolHistory && toolCallsDisabled(envelope) {
		if index := slices.Index(requirements.Features, "tools"); index >= 0 {
			requirements.Features = slices.Delete(requirements.Features, index, index+1)
		}
	}
	if raw, exists := envelope["reasoning_effort"]; exists {
		if operation == config.OperationAnthropic {
			return requirements, errors.New("reasoning_effort is not supported on the Messages interface; use its native thinking parameter")
		}
		if json.Unmarshal(raw, &requirements.ReasoningEffort) != nil || strings.TrimSpace(requirements.ReasoningEffort) == "" || !config.ValidReasoningEffort(requirements.ReasoningEffort) {
			return requirements, errors.New("reasoning_effort is invalid")
		}
		requirements.ReasoningEffort = strings.ToLower(strings.TrimSpace(requirements.ReasoningEffort))
	}
	if raw, exists := envelope["reasoning"]; exists && operation == config.OperationOpenAIResponses {
		var reasoning map[string]json.RawMessage
		if json.Unmarshal(raw, &reasoning) != nil {
			return requirements, errors.New("reasoning must be an object")
		}
		if rawEffort, exists := reasoning["effort"]; exists {
			var effort string
			if json.Unmarshal(rawEffort, &effort) != nil || strings.TrimSpace(effort) == "" || !config.ValidReasoningEffort(effort) {
				return requirements, errors.New("reasoning.effort is invalid")
			}
			effort = strings.ToLower(strings.TrimSpace(effort))
			if requirements.ReasoningEffort != "" && requirements.ReasoningEffort != effort {
				return requirements, errors.New("reasoning.effort conflicts with reasoning_effort")
			}
			requirements.ReasoningEffort = effort
		}
	}
	return requirements, nil
}

func validateRouteRequirements(route config.Route, requirements RequestRequirements) error {
	requested := requirements.ReasoningEffort
	if requested != "" && requested != "auto" && route.ReasoningEffort != "" && route.ReasoningEffort != "auto" && requested != route.ReasoningEffort {
		return errors.New("requested reasoning effort conflicts with the configured model alias")
	}
	return nil
}

func containsImageInput(raw []byte) bool {
	return containsRequestMarker(raw, false)
}

func toolCallsDisabled(envelope map[string]json.RawMessage) bool {
	raw, exists := envelope["tool_choice"]
	if !exists {
		raw = envelope["function_call"]
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte(`"none"`)) {
		return true
	}
	if len(raw) == 0 || len(raw) > 1024 || raw[0] != '{' {
		return false
	}
	var choice struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &choice) == nil && choice.Type == "none"
}

func containsRequestMarker(raw []byte, toolHistory bool) bool {
	// The envelope has already passed JSON validation. Skip arbitrarily large
	// strings without allocating them; decode only short field/type strings.
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		start := index
		index++
		for index < len(raw) {
			if raw[index] == '\\' {
				index += 2
				continue
			}
			if raw[index] == '"' {
				break
			}
			index++
		}
		if index >= len(raw) || index-start > 64 {
			continue
		}
		var key string
		if json.Unmarshal(raw[start:index+1], &key) != nil {
			continue
		}
		next := index + 1
		for next < len(raw) && (raw[next] == ' ' || raw[next] == '\n' || raw[next] == '\r' || raw[next] == '\t') {
			next++
		}
		if next >= len(raw) || raw[next] != ':' {
			continue
		}
		if toolHistory {
			if key == "tool_calls" || key == "function_call" {
				return true
			}
			if key != "type" && key != "role" {
				continue
			}
		} else {
			if key == "image_url" || key == "input_image" {
				return true
			}
			if key != "type" {
				continue
			}
		}
		next++
		for next < len(raw) && (raw[next] == ' ' || raw[next] == '\n' || raw[next] == '\r' || raw[next] == '\t') {
			next++
		}
		if next >= len(raw) || raw[next] != '"' {
			continue
		}
		end := next + 1
		for end < len(raw) && end-next <= 64 && raw[end] != '"' {
			if raw[end] == '\\' {
				end++
			}
			end++
		}
		if end < len(raw) && end-next <= 64 {
			var value string
			if json.Unmarshal(raw[next:end+1], &value) == nil {
				if !toolHistory && (value == "image" || value == "image_url" || value == "input_image") {
					return true
				}
				if toolHistory && key == "role" && (value == "tool" || value == "function") {
					return true
				}
				if toolHistory && key == "type" {
					switch value {
					case "tool_use", "tool_result", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
						return true
					}
				}
			}
		}
	}
	return false
}

// requirementsCompatible preserves unknowns as permission to try, not evidence
// of support. Explicit negative facts always exclude a candidate, including a
// fallback. Facts are scoped to the actual interface and mapped upstream ID.
func requirementsCompatible(account config.Account, upstreamModel, operation string, requirements RequestRequirements, scope ...string) bool {
	credential, effort := "", requirements.ReasoningEffort
	if len(scope) > 0 {
		credential = scope[0]
	}
	if len(scope) > 1 {
		effort = scope[1]
	}
	for _, capability := range account.Capabilities {
		if capability.UpstreamModel == upstreamModel && capability.Evidence.Status == "unsupported" {
			return false
		}
	}
	for _, feature := range append([]string{"call"}, requirements.Features...) {
		for _, capability := range account.Capabilities {
			if capability.UpstreamModel != upstreamModel {
				continue
			}
			for _, key := range []string{
				config.CapabilityFeatureKey(operation, feature, "auto", ""),
				config.CapabilityFeatureKey(operation, feature, effort, ""),
				config.CapabilityFeatureKey(operation, feature, "auto", credential),
				config.CapabilityFeatureKey(operation, feature, effort, credential),
			} {
				if capability.Features[key].Status == "unsupported" {
					return false
				}
			}
		}
	}
	return true
}

func resolveRequestedReasoning(account config.Account, route config.Route, target config.RouteTarget, requirements RequestRequirements) (string, string, bool) {
	requested := requirements.ReasoningEffort
	if requested == "auto" {
		requested = ""
	}
	if requested != "" {
		configured := route.ReasoningEffort
		if route.Model == "" {
			configured = target.ReasoningEffort
		}
		if configured != "" && configured != "auto" && configured != requested {
			return "", "", false
		}
		if route.Model != "" {
			route.ReasoningEffort = requested
		}
	}
	model, effort, ok := config.ResolveRouteTarget(account, route, target)
	if !ok || requested == "" || route.Model != "" {
		return model, effort, ok
	}
	if !supportsRequestedReasoning(account, model, requested) {
		return "", "", false
	}
	return model, requested, true
}

func supportsRequestedReasoning(account config.Account, upstreamModel, requested string) bool {
	known := false
	for _, capability := range account.Capabilities {
		if capability.UpstreamModel != upstreamModel {
			continue
		}
		// A container created to retain a check is not an exhaustive provider
		// declaration of accepted reasoning values.
		if capability.Evidence.Status != "unknown" {
			known = true
		}
		if slices.Contains(capability.ReasoningEfforts, requested) {
			return true
		}
	}
	return !known
}

func requestUpstreamModel(account *AccountRuntime, requested, routeModel string, requirements RequestRequirements) string {
	if requirements.ReasoningEffort != "" && requirements.ReasoningEffort != "auto" && routeModel == "" && account.Config.ModelMap[requested] == "" {
		for _, capability := range account.Config.Capabilities {
			if capability.Model == requested && capability.Evidence.Status != "unsupported" && slices.Contains(capability.ReasoningEfforts, requirements.ReasoningEffort) {
				return capability.UpstreamModel
			}
		}
	}
	return account.upstreamModel(requested, routeModel)
}

var ErrKnownUnavailable = errors.New("eligible sources are temporarily unavailable or cooling down")
