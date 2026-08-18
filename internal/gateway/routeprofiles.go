package gateway

import (
	"encoding/json"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// applyRouteExecutionProfile mutates the already validated request envelope
// after a concrete target has been selected. Execution profiles never change
// the real upstream model chosen by ResolveRouteTarget; they only add request-
// level controls supported by that target.
func applyRouteExecutionProfile(envelope map[string]json.RawMessage, requestedModel, operation string, routes map[string]config.Route, account config.Account) {
	if envelope == nil {
		return
	}
	logicalModel := strings.TrimSpace(requestedModel)
	if route, ok := routes[requestedModel]; ok && strings.TrimSpace(route.Model) != "" {
		logicalModel = strings.TrimSpace(route.Model)
	}
	_, fast := config.ParseRouteModelProfile(logicalModel)
	if !fast {
		return
	}
	if operation != config.OperationOpenAIChat && operation != config.OperationOpenAIResponses {
		return
	}

	// CLIProxy's rich Codex model catalog advertises Fast using service-tier id
	// "priority". The public OpenAI API accepts both priority and fast; other
	// compatible providers receive the user-facing spelling "fast".
	tier := "fast"
	if strings.EqualFold(strings.TrimSpace(account.AdapterID), "cli-proxy-api") {
		tier = "priority"
	}
	encoded, err := json.Marshal(tier)
	if err == nil {
		envelope["service_tier"] = encoded
	}
}
