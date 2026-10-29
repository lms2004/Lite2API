package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CapabilityEvidence describes a scoped fact, not current availability. A model
// catalog is a declaration; only a completed test of this combination can be
// recorded as verified. Empty evidence on old configurations means an operator
// declaration, never a verified call.
type CapabilityEvidence struct {
	Status           string `json:"status,omitempty"`
	Source           string `json:"source,omitempty"`
	ObservedAt       string `json:"observed_at,omitempty"`
	SourceIdentity   string `json:"source_identity,omitempty"`
	UpstreamModel    string `json:"upstream_model,omitempty"`
	CredentialID     string `json:"credential_id,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
	LastCheckOutcome string `json:"last_check_outcome,omitempty"`
	LastCheckedAt    string `json:"last_checked_at,omitempty"`
	LastCheckMessage string `json:"last_check_message,omitempty"`
}

// AccountSourceIdentity binds evidence to the effective connection and frozen
// authentication identity. It is a digest, never a credential export.
func AccountSourceIdentity(account Account) string {
	identity := struct {
		Type, AdapterID, InstanceID, BaseURL, APIKey, AuthHeader, AuthScheme, ProxyURL string
		Headers                                                                        map[string]string
	}{account.Type, account.AdapterID, account.InstanceID, account.BaseURL, account.ResolvedAPIKey(), account.AuthHeader, account.AuthScheme, account.ProxyURL, account.ResolvedHeaders()}
	encoded, _ := json.Marshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func CapabilityFeatureKey(operation, feature, effort, credential string) string {
	key := operation + ":" + feature
	if effort != "" && effort != "auto" {
		key += "#" + effort
	}
	if credential != "" {
		key += "@" + credential
	}
	return key
}

func ParseCapabilityFeatureKey(key string) (operation, feature, effort, credential string, valid bool) {
	operation, rest, ok := strings.Cut(key, ":")
	if !ok || !ValidOperation(operation) {
		return "", "", "", "", false
	}
	rest, credential, _ = strings.Cut(rest, "@")
	feature, effort, _ = strings.Cut(rest, "#")
	if effort == "" {
		effort = "auto"
	}
	valid = ValidCapabilityFeature(feature) && ValidReasoningEffort(effort) && (credential == "" || ValidPublicCredentialID(credential)) && key == CapabilityFeatureKey(operation, feature, effort, credential)
	return
}

func ValidPublicCredentialID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 8 && value == strings.ToLower(value)
}

func invalidateAccountCapabilityEvidence(account *Account) {
	identity := AccountSourceIdentity(*account)
	invalidate := func(evidence *CapabilityEvidence, model string) {
		if evidence.Status != "verified" && evidence.SourceIdentity == "" {
			return
		}
		if evidence.SourceIdentity != identity || evidence.UpstreamModel != model {
			evidence.Status = "unknown"
			// Keep the invalidated fact attached to the current capability scope so
			// a stale upstream identifier cannot later exclude another model.
			evidence.UpstreamModel = model
			evidence.LastCheckOutcome = "inconclusive"
			evidence.LastCheckMessage = "source or model changed; repeat the scoped verification"
		}
	}
	for index := range account.Capabilities {
		capability := &account.Capabilities[index]
		invalidate(&capability.Evidence, capability.UpstreamModel)
		for key, evidence := range capability.Features {
			invalidate(&evidence, capability.UpstreamModel)
			capability.Features[key] = evidence
		}
	}
}

func CloneCapabilityFeatures(features map[string]CapabilityEvidence) map[string]CapabilityEvidence {
	if features == nil {
		return nil
	}
	result := make(map[string]CapabilityEvidence, len(features))
	for key, evidence := range features {
		result[key] = evidence
	}
	return result
}

func ValidCapabilityFeature(feature string) bool {
	switch feature {
	case "call", "stream", "tools", "image_input":
		return true
	}
	return false
}

func validateEvidence(evidence CapabilityEvidence) error {
	if len(evidence.Source) > 512 || len(evidence.ObservedAt) > 64 {
		return fmt.Errorf("capability evidence exceeds size limit")
	}
	if len(evidence.LastCheckMessage) > 512 || len(evidence.UpstreamModel) > MaxModelIDBytes || (evidence.SourceIdentity != "" && len(evidence.SourceIdentity) != 64) || (evidence.CredentialID != "" && !ValidPublicCredentialID(evidence.CredentialID)) || !ValidReasoningEffort(evidence.ReasoningEffort) {
		return fmt.Errorf("invalid capability evidence scope")
	}
	if evidence.SourceIdentity != "" {
		if _, err := hex.DecodeString(evidence.SourceIdentity); err != nil {
			return fmt.Errorf("invalid source identity digest")
		}
	}
	switch evidence.LastCheckOutcome {
	case "", "passed", "failed", "inconclusive":
	default:
		return fmt.Errorf("invalid capability check outcome")
	}
	switch evidence.Status {
	case "", "unknown", "declared", "verified", "unsupported":
	default:
		return fmt.Errorf("unsupported capability evidence status %q", evidence.Status)
	}
	if evidence.Status == "verified" && (evidence.Source == "" || evidence.ObservedAt == "") {
		return fmt.Errorf("verified capability evidence requires source and observed_at")
	}
	if evidence.ObservedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, evidence.ObservedAt); err != nil {
			return fmt.Errorf("capability observed_at must be RFC3339")
		}
	}
	if evidence.LastCheckedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, evidence.LastCheckedAt); err != nil {
			return fmt.Errorf("last_checked_at must be RFC3339")
		}
	}
	return nil
}

func validateCapabilityEvidence(capability ChannelCapability) error {
	if err := validateEvidence(capability.Evidence); err != nil {
		return err
	}
	if capability.Evidence.UpstreamModel != "" && capability.Evidence.UpstreamModel != capability.UpstreamModel {
		return fmt.Errorf("capability evidence upstream_model must match capability upstream_model")
	}
	if len(capability.Features) > 64 {
		return fmt.Errorf("too many capability feature facts")
	}
	for key, evidence := range capability.Features {
		_, _, effort, credential, ok := ParseCapabilityFeatureKey(key)
		if !ok || credential != evidence.CredentialID || (effort != "auto" && effort != evidence.ReasoningEffort) {
			return fmt.Errorf("invalid feature scope %q; use operation:feature", key)
		}
		if evidence.UpstreamModel != "" && evidence.UpstreamModel != capability.UpstreamModel {
			return fmt.Errorf("feature %q: evidence upstream_model must match capability upstream_model", key)
		}
		if err := validateEvidence(evidence); err != nil {
			return fmt.Errorf("feature %q: %w", key, err)
		}
	}
	return nil
}
