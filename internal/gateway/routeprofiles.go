package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// routeExecutionProfileHandler applies request-level execution modes before the
// normal gateway parser/scheduler. It does not choose an upstream; route
// validation already guarantees that a Fast logical profile only targets
// channels whose discovered capability catalog advertised Fast.
func (g *Gateway) routeExecutionProfileHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		operation, supported := operationForGatewayPath(r.URL.Path)
		if !supported || (operation != config.OperationOpenAIChat && operation != config.OperationOpenAIResponses) {
			next.ServeHTTP(w, r)
			return
		}
		state := g.state.Load()
		if state == nil || r.Body == nil || !g.clientKeys.validCredential(apiBearerToken(r), state.legacyKeyHashes) {
			// Keep authentication behavior centralized in ServeGateway. In
			// particular, never consume an unauthenticated body in middleware.
			next.ServeHTTP(w, r)
			return
		}
		limit := state.cfg.Server.MaxBodyBytes
		if limit <= 0 {
			limit = config.DefaultMaxBodyBytes
		}
		if r.ContentLength > limit {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		_ = r.Body.Close()
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "unable to read request body", "invalid_request_error")
			return
		}
		if int64(len(body)) > limit {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error")
			return
		}
		updated := applyExecutionProfileToBody(body, state.cfg.Routes)
		r.Body = io.NopCloser(bytes.NewReader(updated))
		r.ContentLength = int64(len(updated))
		next.ServeHTTP(w, r)
	})
}

func applyExecutionProfileToBody(body []byte, routes map[string]config.Route) []byte {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	var requestedModel string
	if json.Unmarshal(envelope["model"], &requestedModel) != nil || strings.TrimSpace(requestedModel) == "" {
		return body
	}
	logicalModel := strings.TrimSpace(requestedModel)
	if route, ok := routes[requestedModel]; ok && strings.TrimSpace(route.Model) != "" {
		logicalModel = strings.TrimSpace(route.Model)
	}
	_, fast := config.ParseRouteModelProfile(logicalModel)
	if !fast {
		return body
	}
	// OpenAI accepts both `priority` and the renamed `fast`. CLIProxy's current
	// Codex translator intentionally preserves `priority`, so this compatibility
	// spelling works for both official OpenAI and CLIProxy-backed Codex routes.
	encoded, _ := json.Marshal("priority")
	envelope["service_tier"] = encoded
	updated, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return updated
}
