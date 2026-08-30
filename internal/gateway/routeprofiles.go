package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// routeExecutionProfileHandler is intentionally body-agnostic. Execution
// profiles are applied by ServeGateway after authentication and admission,
// using the same decoded envelope that is forwarded upstream. Keeping this
// wrapper makes the server wiring backwards-compatible without creating a
// second request-body reader or a second runtime-state generation per request.
func (g *Gateway) routeExecutionProfileHandler(next http.Handler) http.Handler {
	return next
}

func applyExecutionProfileToBody(body []byte, routes map[string]config.Route) []byte {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if !applyExecutionProfile(envelope, routes) {
		return body
	}
	updated, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return updated
}

// applyExecutionProfile mutates an already decoded request envelope and
// reports whether it changed. It is deliberately free of I/O so callers can
// enforce admission before allocating or reading a potentially large body.
func applyExecutionProfile(envelope map[string]json.RawMessage, routes map[string]config.Route) bool {
	var requestedModel string
	if json.Unmarshal(envelope["model"], &requestedModel) != nil || strings.TrimSpace(requestedModel) == "" {
		return false
	}
	logicalModel := strings.TrimSpace(requestedModel)
	if route, ok := routes[requestedModel]; ok && strings.TrimSpace(route.Model) != "" {
		logicalModel = strings.TrimSpace(route.Model)
	}
	_, fast := config.ParseRouteModelProfile(logicalModel)
	if !fast {
		return false
	}
	// OpenAI accepts both `priority` and the renamed `fast`. CLIProxy's current
	// Codex translator intentionally preserves `priority`, so this compatibility
	// spelling works for both official OpenAI and CLIProxy-backed Codex routes.
	encoded, _ := json.Marshal("priority")
	envelope["service_tier"] = encoded
	return true
}
