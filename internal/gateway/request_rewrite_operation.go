package gateway

import (
	"encoding/json"

	"github.com/lms2004/lite2api/internal/config"
)

func operationRequestRewriteRequired(operation string, original []byte, envelope map[string]json.RawMessage, requestedModel, model, effort string) bool {
	if operation == config.OperationOpenAIResponses {
		if _, exists := envelope["reasoning_effort"]; exists {
			return true
		}
	}
	return requestRewriteRequired(original, requestedModel, model, effort)
}

func rewriteOperationRequestBody(operation string, original []byte, envelope map[string]json.RawMessage, requestedModel, model, effort string) ([]byte, error) {
	if operation != config.OperationOpenAIResponses {
		return rewriteRequestBody(original, envelope, requestedModel, model, effort)
	}
	if !operationRequestRewriteRequired(operation, original, envelope, requestedModel, model, effort) {
		return original, nil
	}
	copyEnvelope := make(map[string]json.RawMessage, len(envelope))
	for key, value := range envelope {
		copyEnvelope[key] = value
	}
	copyEnvelope["model"], _ = json.Marshal(model)
	delete(copyEnvelope, "reasoning_effort")
	if effort != "" && effort != "auto" {
		var reasoning map[string]json.RawMessage
		if raw := envelope["reasoning"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &reasoning); err != nil {
				return nil, err
			}
		}
		if reasoning == nil {
			reasoning = make(map[string]json.RawMessage)
		}
		reasoning["effort"], _ = json.Marshal(effort)
		copyEnvelope["reasoning"], _ = json.Marshal(reasoning)
	}
	return json.Marshal(copyEnvelope)
}
