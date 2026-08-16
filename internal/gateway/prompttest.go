package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

const (
	maxPromptTestMessages = 64
	maxPromptTestContent  = 256 << 10
	maxPromptTestResponse = 8 << 20
)

type promptTestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type promptTestRequest struct {
	AccountID   string              `json:"account_id"`
	Model       string              `json:"model"`
	Messages    []promptTestMessage `json:"messages"`
	Temperature *float64            `json:"temperature,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

func (g *Gateway) servePromptTest(w http.ResponseWriter, r *http.Request, state *runtimeState) {
	var input promptTestRequest
	if err := decodeAdminJSON(w, r, &input); err != nil {
		return
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.Model = strings.TrimSpace(input.Model)
	if input.AccountID == "" || len(input.AccountID) > 64 {
		writeAPIErrorCode(w, http.StatusBadRequest, "account_id is required", "invalid_request_error", "invalid_prompt_test_account")
		return
	}
	if input.Model == "" || len(input.Model) > 256 {
		writeAPIErrorCode(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_prompt_test_model")
		return
	}
	if len(input.Messages) == 0 || len(input.Messages) > maxPromptTestMessages {
		writeAPIErrorCode(w, http.StatusBadRequest, "messages must contain between 1 and 64 items", "invalid_request_error", "invalid_prompt_test_messages")
		return
	}
	totalContent := 0
	for i := range input.Messages {
		input.Messages[i].Role = strings.TrimSpace(input.Messages[i].Role)
		if input.Messages[i].Role != "user" && input.Messages[i].Role != "assistant" && input.Messages[i].Role != "system" {
			writeAPIErrorCode(w, http.StatusBadRequest, "message role must be user, assistant, or system", "invalid_request_error", "invalid_prompt_test_role")
			return
		}
		if strings.TrimSpace(input.Messages[i].Content) == "" {
			writeAPIErrorCode(w, http.StatusBadRequest, "message content cannot be empty", "invalid_request_error", "invalid_prompt_test_content")
			return
		}
		totalContent += len(input.Messages[i].Content)
	}
	if totalContent > maxPromptTestContent {
		writeAPIErrorCode(w, http.StatusRequestEntityTooLarge, "test conversation is too large", "invalid_request_error", "prompt_test_too_large")
		return
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		writeAPIErrorCode(w, http.StatusBadRequest, "temperature must be between 0 and 2", "invalid_request_error", "invalid_prompt_test_temperature")
		return
	}
	if input.MaxTokens < 0 || input.MaxTokens > 32768 {
		writeAPIErrorCode(w, http.StatusBadRequest, "max_tokens must be between 0 and 32768", "invalid_request_error", "invalid_prompt_test_max_tokens")
		return
	}

	account := state.scheduler.Get(input.AccountID)
	if account == nil {
		writeAPIErrorCode(w, http.StatusNotFound, "selected account was not found", "invalid_request_error", "prompt_test_account_not_found")
		return
	}
	if !account.Config.Enabled {
		writeAPIErrorCode(w, http.StatusConflict, "selected account is disabled", "invalid_request_error", "prompt_test_account_disabled")
		return
	}
	operation := config.OperationOpenAIChat
	if !config.AccountSupportsOperation(account.Config, operation) {
		operation = config.OperationAnthropic
	}
	if !config.AccountSupportsOperation(account.Config, operation) {
		writeAPIErrorCode(w, http.StatusBadRequest, "selected account does not support a chat operation", "invalid_request_error", "prompt_test_operation_unsupported")
		return
	}
	if !account.tryAcquire() {
		w.Header().Set("Retry-After", "1")
		writeAPIErrorCode(w, http.StatusTooManyRequests, "selected account has reached its concurrency limit", "rate_limit_error", "prompt_test_concurrency_limit")
		return
	}
	defer account.release()

	upstreamModel := account.upstreamModel(input.Model, "")
	path := "/v1/chat/completions"
	payload := map[string]any{"model": upstreamModel, "messages": input.Messages, "stream": false}
	if operation == config.OperationAnthropic {
		path = "/v1/messages"
		messages := make([]promptTestMessage, 0, len(input.Messages))
		systemParts := make([]string, 0, 1)
		for _, message := range input.Messages {
			if message.Role == "system" {
				systemParts = append(systemParts, message.Content)
				continue
			}
			messages = append(messages, message)
		}
		payload = map[string]any{"model": upstreamModel, "messages": messages, "stream": false}
		if len(systemParts) > 0 {
			payload["system"] = strings.Join(systemParts, "\n\n")
		}
		if input.MaxTokens == 0 {
			input.MaxTokens = 1024
		}
	}
	if input.Temperature != nil {
		payload["temperature"] = *input.Temperature
	}
	if input.MaxTokens > 0 {
		payload["max_tokens"] = input.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to encode test request", "gateway_error")
		return
	}

	upstreamInbound := r.Clone(r.Context())
	upstreamInbound.Method = http.MethodPost
	upstreamInbound.URL = &url.URL{Path: path}
	upstreamInbound.Header = make(http.Header)
	requestID := requestID()
	started := time.Now()
	resp, err := g.doUpstream(r.Context(), state, account, upstreamInbound, body, requestID)
	if err != nil {
		writeAPIErrorCode(w, http.StatusBadGateway, "upstream test request failed", "upstream_error", "prompt_test_upstream_failed")
		return
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxPromptTestResponse+1))
	latency := time.Since(started)
	if readErr != nil {
		writeAPIErrorCode(w, http.StatusBadGateway, "failed to read upstream test response", "upstream_error", "prompt_test_response_failed")
		return
	}
	if len(responseBody) > maxPromptTestResponse {
		writeAPIErrorCode(w, http.StatusBadGateway, "upstream test response exceeded 8 MiB", "upstream_error", "prompt_test_response_too_large")
		return
	}
	if resp.StatusCode >= 400 {
		var upstream any
		if json.Unmarshal(responseBody, &upstream) != nil {
			upstream = strings.TrimSpace(string(responseBody))
		}
		writeJSON(w, resp.StatusCode, map[string]any{
			"error":    map[string]any{"message": "upstream returned " + resp.Status, "type": "upstream_error", "code": "prompt_test_upstream_status"},
			"upstream": upstream,
		})
		return
	}
	responseBody = bytes.TrimSpace(responseBody)
	if !json.Valid(responseBody) {
		writeAPIErrorCode(w, http.StatusBadGateway, "upstream returned a non-JSON response", "upstream_error", "prompt_test_invalid_response")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id":     input.AccountID,
		"upstream_model": upstreamModel,
		"request_id":     requestID,
		"latency_ms":     latency.Milliseconds(),
		"response":       json.RawMessage(responseBody),
	})
}
