package config

import "testing"

func TestNormalizeOperationsAndEmptyAccounts(t *testing.T) {
	cfg := Normalize(Config{Server: Defaults().Server})
	if cfg.Accounts == nil {
		t.Fatal("empty accounts must normalize to [] instead of null")
	}
	account := Account{Type: "anthropic"}
	if !AccountSupportsOperation(account, OperationAnthropic) || AccountSupportsOperation(account, OperationOpenAIChat) {
		t.Fatal("legacy anthropic operation defaults are incorrect")
	}
	account = Account{Type: "openai"}
	if !AccountSupportsOperation(account, OperationOpenAIChat) || AccountSupportsOperation(account, OperationAnthropic) {
		t.Fatal("legacy openai operation defaults are incorrect")
	}
}

func TestValidateRejectsUnknownOperation(t *testing.T) {
	cfg := Defaults()
	cfg.Accounts = []Account{{
		ID: "bad", Type: "openai", BaseURL: "https://example.com/v1",
		APIKey: "test", Enabled: true, Operations: []string{"unknown.operation"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown operation was accepted")
	}
}
