package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type capabilityVerificationRequest struct {
	AccountID       string `json:"account_id"`
	Model           string `json:"model"`
	Operation       string `json:"operation,omitempty"`
	Feature         string `json:"feature,omitempty"`
	CredentialID    string `json:"credential_id,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type capabilityVerificationResult struct {
	OK              bool   `json:"ok"`
	Completed       bool   `json:"completed"`
	EvidenceSaved   bool   `json:"evidence_saved"`
	AccountID       string `json:"account_id"`
	UpstreamModel   string `json:"upstream_model"`
	Operation       string `json:"operation"`
	Feature         string `json:"feature"`
	CredentialID    string `json:"credential_id,omitempty"`
	ReasoningEffort string `json:"reasoning_effort"`
	ObservedAt      string `json:"observed_at"`
	RequestID       string `json:"request_id"`
	Message         string `json:"message"`
}

type capabilityVerification struct {
	account                                                     config.Account
	identity                                                    string
	logicalModel, model, operation, feature, effort, credential string
	requestID                                                   string
}

func newCapabilityVerification(account *AccountRuntime, logicalModel, model, operation, feature, effort, credential, id string) capabilityVerification {
	if effort == "" {
		effort = "auto"
	}
	return capabilityVerification{
		account: cloneAccount(account.Config), identity: fmt.Sprintf("%x", account.state.identity),
		logicalModel: logicalModel, model: model, operation: operation, feature: feature, effort: effort, credential: credential, requestID: id,
	}
}

func (g *Gateway) serveCapabilityVerification(w http.ResponseWriter, r *http.Request, state *runtimeState) {
	var input capabilityVerificationRequest
	if decodeAdminJSON(w, r, &input) != nil {
		return
	}
	input.AccountID, input.Model = strings.TrimSpace(input.AccountID), strings.TrimSpace(input.Model)
	input.Operation, input.Feature = strings.TrimSpace(input.Operation), strings.TrimSpace(input.Feature)
	input.ReasoningEffort = strings.ToLower(strings.TrimSpace(input.ReasoningEffort))
	if input.Model == "" || input.Model == "*" || len(input.Model) > config.MaxModelIDBytes || input.AccountID == "" || len(input.AccountID) > config.MaxAccountIDBytes {
		writeAPIErrorCode(w, 400, "account_id and model are required", "invalid_request_error", "invalid_capability_verification")
		return
	}
	account := state.scheduler.Get(input.AccountID)
	if account == nil || !account.Config.Enabled {
		writeAPIErrorCode(w, 409, "verification requires an enabled configured source", "invalid_request_error", "verification_source_unavailable")
		return
	}
	if input.Operation == "" {
		for _, operation := range []string{config.OperationOpenAIChat, config.OperationOpenAIResponses, config.OperationAnthropic} {
			if config.AccountSupportsOperation(account.Config, operation) {
				input.Operation = operation
				break
			}
		}
	}
	if input.Feature == "" {
		input.Feature = "call"
	}
	if _, fast := config.ParseRouteModelProfile(input.Model); fast && input.Operation == config.OperationAnthropic {
		writeAPIErrorCode(w, 400, "fast execution profiles are not mapped to the Messages interface", "invalid_request_error", "verification_scope_unsupported")
		return
	}
	if input.ReasoningEffort == "" {
		input.ReasoningEffort = "auto"
	}
	if (input.Operation != config.OperationOpenAIChat && input.Operation != config.OperationOpenAIResponses && input.Operation != config.OperationAnthropic) || !config.AccountSupportsOperation(account.Config, input.Operation) ||
		(input.Feature != "call" && input.Feature != "stream" && input.Feature != "tools" && input.Feature != "image_input") || !config.ValidReasoningEffort(input.ReasoningEffort) ||
		(input.Operation == config.OperationAnthropic && input.ReasoningEffort != "auto") {
		writeAPIErrorCode(w, 400, "verification supports call, stream, image input or tool round trips on a declared Chat, Responses or Messages interface; Messages uses native thinking", "invalid_request_error", "verification_scope_unsupported")
		return
	}
	if input.CredentialID != "" && (!config.ValidPublicCredentialID(input.CredentialID) || !trustedOAuthAccount(account.Config)) {
		writeAPIErrorCode(w, 400, "credential_id requires the trusted local OAuth adapter and a public credential index", "invalid_request_error", "invalid_verification_credential")
		return
	}
	requirements := RequestRequirements{ReasoningEffort: input.ReasoningEffort}
	model := requestUpstreamModel(account, input.Model, "", requirements)
	if model == "*" || !account.supports(input.Model) && !config.AccountSupportsTargetModel(account.Config, input.Model) && account.Config.ModelMap[input.Model] == "" ||
		!config.AccountSupportsTargetModel(account.Config, model) || input.ReasoningEffort != "auto" && !supportsRequestedReasoning(account.Config, model, input.ReasoningEffort) {
		writeAPIErrorCode(w, 400, "the selected source does not declare this model and reasoning combination", "invalid_request_error", "verification_model_unsupported")
		return
	}
	releaseAdmission, admitted := g.admitMaintenanceCall(w, state)
	if !admitted {
		return
	}
	defer releaseAdmission()
	if !account.tryAcquire() {
		writeAPIErrorCode(w, 429, "the selected source is at its concurrency limit", "rate_limit_error", "verification_concurrency_limit")
		return
	}
	defer state.scheduler.releaseAccount(account)
	check := newCapabilityVerification(account, input.Model, model, input.Operation, input.Feature, input.ReasoningEffort, input.CredentialID, requestID())
	ctx, cancel := context.WithTimeout(r.Context(), maxPromptTestDuration)
	defer cancel()
	completed, credential, probeErr := g.runCapabilityProbe(ctx, state, account, check)
	check.credential = credential
	result := g.finishCapabilityVerification(check, completed && probeErr == nil, completed, probeErr)
	writeJSON(w, http.StatusOK, result)
}

func (g *Gateway) finishCapabilityVerification(check capabilityVerification, passed, completed bool, probeErr error) capabilityVerificationResult {
	result := capabilityVerificationResult{OK: passed, Completed: completed, AccountID: check.account.ID, UpstreamModel: check.model, Operation: check.operation, Feature: check.feature, CredentialID: check.credential, ReasoningEffort: check.effort, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), RequestID: check.requestID}
	result.Message = "exact source, model and interface completed the requested verification"
	if !passed {
		result.Message = "verification did not establish this capability; failure is not proof of unsupported behavior"
		if probeErr != nil {
			result.Message = truncate(probeErr.Error(), 512)
		}
	}
	if check.model == "*" {
		result.Message += "; evidence requires a concrete upstream model and was not saved"
		return result
	}
	if strings.EqualFold(check.account.AdapterID, "cli-proxy-api") && !config.ValidPublicCredentialID(check.credential) {
		result.Message += "; evidence was not saved because the adapter did not attest a public credential identity"
		return result
	}
	sourceChanged := false
	err := g.updateConfig(func(cfg *config.Config) (bool, error) {
		index := accountIndex(cfg.Accounts, check.account.ID)
		if index < 0 || !verificationSourceMatches(check, cfg.Accounts[index]) {
			sourceChanged = true
			return false, nil
		}
		account := &cfg.Accounts[index]
		capIndex := -1
		for index, capability := range account.Capabilities {
			if capability.Model == check.logicalModel && capability.UpstreamModel == check.model {
				capIndex = index
				break
			}
		}
		if capIndex < 0 {
			if len(account.Capabilities) >= config.MaxCapabilitiesPerAccount {
				return false, errors.New("capability evidence limit reached")
			}
			account.Capabilities = append(account.Capabilities, config.ChannelCapability{Model: check.logicalModel, UpstreamModel: check.model, ReasoningEfforts: []string{"auto"}, Evidence: config.CapabilityEvidence{Status: "unknown", Source: "verification_scope"}})
			capIndex = len(account.Capabilities) - 1
		}
		capability := &account.Capabilities[capIndex]
		if passed && capability.Evidence.Source == "verification_scope" && !slices.Contains(capability.ReasoningEfforts, check.effort) {
			capability.ReasoningEfforts = append(capability.ReasoningEfforts, check.effort)
		}
		if capability.Features == nil {
			capability.Features = make(map[string]config.CapabilityEvidence)
		}
		key := config.CapabilityFeatureKey(check.operation, check.feature, check.effort, check.credential)
		evidence, exists := capability.Features[key]
		if !exists && len(capability.Features) >= 64 {
			return false, errors.New("scoped capability evidence limit reached")
		}
		// A failed check must not erase an explicit unsupported declaration. The
		// declaration is a scoped exclusion; a later successful check may replace
		// it, while an inconclusive failure only records the latest observation.
		if passed || evidence.Status != "verified" && evidence.Status != "unsupported" {
			evidence = config.CapabilityEvidence{Status: "unknown", Source: "capability_test:" + check.requestID, ObservedAt: result.ObservedAt, SourceIdentity: check.identity, UpstreamModel: check.model, CredentialID: check.credential, ReasoningEffort: check.effort}
			if passed {
				evidence.Status = "verified"
			}
		}
		evidence.LastCheckOutcome, evidence.LastCheckedAt, evidence.LastCheckMessage = "failed", result.ObservedAt, result.Message
		if passed {
			evidence.LastCheckOutcome = "passed"
		}
		capability.Features[key] = evidence
		return true, nil
	})
	if err != nil {
		result.Message += "; evidence was not saved because configuration changed or persistence failed; refresh the source and repeat verification"
		return result
	}
	if sourceChanged {
		result.Message += "; source or model mapping changed during verification, so the old result was not saved"
		return result
	}
	result.EvidenceSaved = true
	return result
}

func verificationSourceMatches(check capabilityVerification, current config.Account) bool {
	if !current.Enabled || config.AccountSourceIdentity(current) != check.identity || !config.AccountSupportsOperation(current, check.operation) || !config.AccountSupportsTargetModel(current, check.model) {
		return false
	}
	requirements := RequestRequirements{ReasoningEffort: check.effort, Features: []string{check.feature}}
	if requestUpstreamModel(&AccountRuntime{Config: current}, check.logicalModel, "", requirements) != check.model || check.effort != "auto" && !supportsRequestedReasoning(current, check.model, check.effort) {
		return false
	}
	// Preserve a concurrently added restriction. Unrelated catalogs, metadata,
	// and successful checks do not invalidate a result for this exact scope.
	wasAllowed := requirementsCompatible(check.account, check.model, check.operation, requirements, check.credential, check.effort)
	isAllowed := requirementsCompatible(current, check.model, check.operation, requirements, check.credential, check.effort)
	return !wasAllowed || isAllowed
}
