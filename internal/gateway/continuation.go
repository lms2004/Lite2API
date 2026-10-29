package gateway

import (
	"errors"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

// ValidateContinuation permits native HTTP continuation only when this runtime
// generation has a single fixed direct upstream. Availability and capacity do
// not reduce the candidate set: a cooling original owner cannot silently turn
// a different source into a safe continuation target. The native upstream owns
// and validates the opaque response ID; Lite2API does not reconstruct history.
func (s *Scheduler) ValidateContinuation(model, operation string, requirements RequestRequirements) error {
	if !requirements.Continuation {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	route, routed := s.routes[model]
	type owner struct {
		accountID, upstreamModel, credential string
	}
	owners := make(map[owner]*AccountRuntime)
	stable := true
	add := func(account *AccountRuntime, upstreamModel, credential string, fixedModel bool, effort string) {
		if !requirementsCompatible(account.Config, upstreamModel, operation, requirements, credential, effort) {
			return
		}
		owners[owner{account.Config.ID, upstreamModel, credential}] = account
		stable = stable && fixedModel
	}
	if routed && len(route.Targets) > 0 {
		for _, target := range route.Targets {
			account := s.accounts[target.Account]
			if account == nil || !account.Config.Enabled || !config.AccountSupportsOperation(account.Config, operation) {
				continue
			}
			upstreamModel, effort, compatible := resolveRequestedReasoning(account.Config, route, target, requirements)
			if !compatible {
				continue
			}
			fixedModel := route.Model == "" || stableLogicalModel(account.Config, route.Model)
			add(account, upstreamModel, target.Credential, fixedModel, effort)
		}
	} else {
		allowed := make(map[string]struct{}, len(route.Accounts))
		for _, id := range route.Accounts {
			allowed[id] = struct{}{}
		}
		for id, account := range s.accounts {
			if !account.Config.Enabled || !config.AccountSupportsOperation(account.Config, operation) {
				continue
			}
			if routed && !route.AllAccounts {
				if _, ok := allowed[id]; !ok {
					continue
				}
			}
			if !routed && !account.supports(model) {
				continue
			}
			upstreamModel := requestUpstreamModel(account, model, route.UpstreamModel, requirements)
			if !config.AccountSupportsTargetModel(account.Config, upstreamModel) ||
				(requirements.ReasoningEffort != "" && requirements.ReasoningEffort != "auto" && !supportsRequestedReasoning(account.Config, upstreamModel, requirements.ReasoningEffort)) {
				continue
			}
			fixedModel := route.UpstreamModel != "" || account.Config.ModelMap[model] != "" || stableLogicalModel(account.Config, model)
			add(account, upstreamModel, "", fixedModel, requirements.ReasoningEffort)
		}
	}
	if len(owners) != 1 || !stable {
		return errors.New("previous_response_id requires one fixed direct upstream; send complete input history for routes with multiple sources or model mappings")
	}
	for owner, account := range owners {
		adapter := strings.ToLower(strings.TrimSpace(account.Config.AdapterID))
		if account.Config.Type != "openai" || (adapter != "" && adapter != "generic-openai") || owner.credential != "" {
			return errors.New("previous_response_id is unsupported by this adapter; send complete input history")
		}
	}
	return nil
}

func stableLogicalModel(account config.Account, logical string) bool {
	upstream := ""
	for _, capability := range account.Capabilities {
		if capability.Model != logical || capability.Evidence.Status == "unsupported" {
			continue
		}
		if upstream != "" && upstream != capability.UpstreamModel {
			return false
		}
		upstream = capability.UpstreamModel
	}
	return true
}
