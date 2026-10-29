package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// capabilityProbeImage is a deliberately tiny, self-contained PNG. The
// probe sends it through each interface's native image shape so a successful
// completion demonstrates that the source accepted an actual image input,
// rather than merely accepting a text request. It is kept inline to avoid
// reading operator-controlled files during an admin verification call.
const capabilityProbeImage = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func capabilityProbePath(operation string) string {
	switch operation {
	case config.OperationOpenAIResponses:
		return "/v1/responses"
	case config.OperationAnthropic:
		return "/v1/messages"
	}
	return "/v1/chat/completions"
}

func capabilityProbePayload(check capabilityVerification, text string) map[string]any {
	payload := map[string]any{"model": check.model, "stream": check.feature == "stream"}
	if _, fast := config.ParseRouteModelProfile(check.logicalModel); fast && check.operation != config.OperationAnthropic {
		payload["service_tier"] = "priority"
	}
	switch check.operation {
	case config.OperationOpenAIResponses:
		payload["input"] = []any{map[string]any{"role": "user", "content": text}}
		payload["max_output_tokens"] = 512
		if check.effort != "auto" {
			payload["reasoning"] = map[string]any{"effort": check.effort}
		}
	case config.OperationAnthropic:
		payload["messages"] = []any{map[string]any{"role": "user", "content": text}}
		payload["max_tokens"] = 512
	default:
		payload["messages"] = []any{map[string]any{"role": "user", "content": text}}
		payload["max_tokens"] = 512
		if check.effort != "auto" {
			payload["reasoning_effort"] = check.effort
		}
	}
	if check.feature == "image_input" {
		// Each protocol has a different native representation. Keep the text
		// part alongside the image so providers that require a mixed content
		// request can prove they parsed the multimodal message correctly.
		switch check.operation {
		case config.OperationOpenAIResponses:
			payload["input"] = []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": text},
				map[string]any{"type": "input_image", "image_url": capabilityProbeImage},
			}}}
		case config.OperationAnthropic:
			payload["messages"] = []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": text},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": strings.TrimPrefix(capabilityProbeImage, "data:image/png;base64,")}},
			}}}
		default:
			payload["messages"] = []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": text},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": capabilityProbeImage}},
			}}}
		}
	}
	return payload
}

// The probe executes a fixed, harmless exchange. A tool capability requires an
// actual named tool invocation, valid arguments, a supplied result and a final
// response containing a nonce that was absent from the first request.
func (g *Gateway) runCapabilityProbe(ctx context.Context, state *runtimeState, account *AccountRuntime, check capabilityVerification) (bool, string, error) {
	if check.feature != "tools" {
		prompt := "Reply briefly with OK."
		if check.feature == "image_input" {
			prompt = "Accept the supplied image and reply briefly with OK."
		}
		payload := capabilityProbePayload(check, prompt)
		_, credential, err := g.executeCapabilityProbe(ctx, state, account, check, payload, check.credential)
		return err == nil, credential, err
	}
	argumentToken, resultToken := requestID(), requestID()
	payload := capabilityProbePayload(check, "Call lite2api_verify exactly once. After receiving its tool result, repeat that result exactly as your final answer.")
	parameters := map[string]any{"type": "object", "properties": map[string]any{"token": map[string]any{"type": "string", "enum": []string{argumentToken}}}, "required": []string{"token"}, "additionalProperties": false}
	function := map[string]any{"name": "lite2api_verify", "description": "Return a harmless verification value.", "parameters": parameters}
	switch check.operation {
	case config.OperationOpenAIResponses:
		payload["tools"] = []any{map[string]any{"type": "function", "name": function["name"], "description": function["description"], "parameters": parameters}}
		payload["tool_choice"] = map[string]any{"type": "function", "name": "lite2api_verify"}
	case config.OperationAnthropic:
		payload["tools"] = []any{map[string]any{"name": "lite2api_verify", "description": function["description"], "input_schema": parameters}}
		payload["tool_choice"] = map[string]any{"type": "tool", "name": "lite2api_verify"}
	default:
		payload["tools"] = []any{map[string]any{"type": "function", "function": function}}
		payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "lite2api_verify"}}
	}
	first, credential, err := g.executeCapabilityProbe(ctx, state, account, check, payload, check.credential)
	if err != nil {
		return false, credential, err
	}
	if strings.EqualFold(account.Config.AdapterID, "cli-proxy-api") && !config.ValidPublicCredentialID(credential) {
		return true, "", errors.New("the adapter did not attest a credential, so a tool round trip could not be pinned to its original source")
	}
	followup, err := capabilityToolFollowup(check, payload, first, argumentToken, resultToken)
	if err != nil {
		return true, credential, err
	}
	second, finalCredential, err := g.executeCapabilityProbe(ctx, state, account, check, followup, credential)
	if err != nil {
		return false, credential, err
	}
	if finalCredential != credential {
		return true, credential, errors.New("the credential changed during the tool round trip; capability was not established")
	}
	if !strings.Contains(capabilityProbeText(check.operation, second), resultToken) {
		return true, credential, errors.New("the final response did not contain the supplied tool result; tool round trip was not established")
	}
	return true, credential, nil
}

func (g *Gateway) executeCapabilityProbe(ctx context.Context, state *runtimeState, account *AccountRuntime, check capabilityVerification, payload map[string]any, credential string) ([]byte, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, credential, errors.New("could not encode the verification request")
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(body, &envelope)
	requirements, err := requestCompletionRequirements(envelope, check.operation)
	if err != nil {
		return nil, credential, errors.New("verification request conflicts with the interface contract")
	}
	inbound := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: capabilityProbePath(check.operation)}, Header: make(http.Header)}
	inbound.Header.Set("Anthropic-Version", "2023-06-01")
	response, err := g.doUpstream(ctx, state, account, inbound, body, check.requestID, credential)
	if err != nil {
		return nil, credential, errors.New("verification transport failed; submission may be uncertain and was not replayed")
	}
	defer response.Body.Close()
	// No fallback value: the actual adapter must attest the identity it used.
	selected := selectedOAuthCredential(account.Config, response, "")
	if strings.EqualFold(account.Config.AdapterID, "cli-proxy-api") {
		if credential != "" && selected != credential {
			return nil, selected, errors.New("adapter did not confirm the requested credential identity")
		}
		credential = selected
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, credential, fmt.Errorf("verification was rejected with HTTP %d; check authorization, quota and the requested combination", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPromptTestResponse+1))
	if err != nil || len(data) > maxPromptTestResponse {
		return nil, credential, errors.New("verification response was interrupted or exceeded its size limit")
	}
	stream, _ := payload["stream"].(bool)
	completion := newResponseCompletion(check.operation, inbound.URL.Path, response.Header.Get("Content-Type"), stream, requirements)
	completion.Feed(data)
	if err := completion.Finish(); err != nil {
		return nil, credential, errors.New("verification did not receive a complete response matching the requested interface and features")
	}
	return data, credential, nil
}

func capabilityToolFollowup(check capabilityVerification, payload map[string]any, data []byte, argumentToken, resultToken string) (map[string]any, error) {
	var response map[string]json.RawMessage
	if json.Unmarshal(data, &response) != nil {
		return nil, errors.New("tool verification did not return a JSON result")
	}
	followup := make(map[string]any, len(payload))
	for key, value := range payload {
		followup[key] = value
	}
	var id, name string
	var arguments json.RawMessage
	switch check.operation {
	case config.OperationOpenAIResponses:
		var output []json.RawMessage
		_ = json.Unmarshal(response["output"], &output)
		input := append([]any(nil), payload["input"].([]any)...)
		for _, item := range output {
			var call struct{ Type, CallID, Name, Arguments string }
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(item, &fields)
			_ = json.Unmarshal(fields["type"], &call.Type)
			_ = json.Unmarshal(fields["call_id"], &call.CallID)
			_ = json.Unmarshal(fields["name"], &call.Name)
			_ = json.Unmarshal(fields["arguments"], &call.Arguments)
			if call.Type == "function_call" {
				if id != "" {
					return nil, errors.New("verification expected exactly one tool call")
				}
				id, name, arguments = call.CallID, call.Name, json.RawMessage(call.Arguments)
			}
			input = append(input, item)
		}
		followup["input"] = append(input, map[string]any{"type": "function_call_output", "call_id": id, "output": resultToken})
		followup["tool_choice"] = "none"
	case config.OperationAnthropic:
		var content []json.RawMessage
		_ = json.Unmarshal(response["content"], &content)
		for _, item := range content {
			var call struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(item, &call)
			if call.Type == "tool_use" {
				if id != "" {
					return nil, errors.New("verification expected exactly one tool call")
				}
				id, name, arguments = call.ID, call.Name, call.Input
			}
		}
		messages := append([]any(nil), payload["messages"].([]any)...)
		followup["messages"] = append(messages, map[string]any{"role": "assistant", "content": content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": resultToken}}})
		followup["tool_choice"] = map[string]any{"type": "auto"}
	default:
		var choices []struct {
			Message json.RawMessage `json:"message"`
		}
		_ = json.Unmarshal(response["choices"], &choices)
		if len(choices) != 1 {
			return nil, errors.New("verification expected exactly one tool response choice")
		}
		var assistant struct {
			ToolCalls []struct {
				ID, Type string
				Function struct{ Name, Arguments string }
			} `json:"tool_calls"`
		}
		_ = json.Unmarshal(choices[0].Message, &assistant)
		if len(assistant.ToolCalls) != 1 {
			return nil, errors.New("verification did not receive the required tool call")
		}
		call := assistant.ToolCalls[0]
		id, name, arguments = call.ID, call.Function.Name, json.RawMessage(call.Function.Arguments)
		messages := append([]any(nil), payload["messages"].([]any)...)
		followup["messages"] = append(messages, choices[0].Message, map[string]any{"role": "tool", "tool_call_id": id, "content": resultToken})
		followup["tool_choice"] = "none"
	}
	var args struct {
		Token string `json:"token"`
	}
	if id == "" || name != "lite2api_verify" || json.Unmarshal(arguments, &args) != nil || args.Token != argumentToken {
		return nil, errors.New("the response did not contain the requested named tool call with its verification arguments")
	}
	return followup, nil
}

func capabilityProbeText(operation string, data []byte) string {
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct{ Type, Text string } `json:"content"`
		Output  []struct {
			Content []struct{ Type, Text string } `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(data, &response) != nil {
		return ""
	}
	var text strings.Builder
	switch operation {
	case config.OperationOpenAIResponses:
		for _, item := range response.Output {
			for _, content := range item.Content {
				if content.Type == "output_text" {
					text.WriteString(content.Text)
				}
			}
		}
	case config.OperationAnthropic:
		for _, item := range response.Content {
			if item.Type == "text" {
				text.WriteString(item.Text)
			}
		}
	default:
		for _, item := range response.Choices {
			text.WriteString(item.Message.Content)
		}
	}
	return text.String()
}
