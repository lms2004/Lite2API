package config

import "strings"

const (
	OperationOpenAIChat      = "openai.chat"
	OperationOpenAIResponses = "openai.responses"
	OperationAnthropic       = "anthropic.messages"
	OperationEmbeddings      = "openai.embeddings"
	OperationImages          = "openai.images"
	OperationRerank          = "openai.rerank"
)

var validOperations = map[string]struct{}{
	OperationOpenAIChat: {}, OperationOpenAIResponses: {}, OperationAnthropic: {},
	OperationEmbeddings: {}, OperationImages: {}, OperationRerank: {},
}

func ValidOperation(operation string) bool {
	_, ok := validOperations[strings.TrimSpace(operation)]
	return ok
}

func DefaultOperations(accountType string) []string {
	if strings.EqualFold(strings.TrimSpace(accountType), "anthropic") {
		return []string{OperationAnthropic}
	}
	return []string{
		OperationOpenAIChat, OperationOpenAIResponses, OperationEmbeddings,
		OperationImages, OperationRerank,
	}
}

func AccountSupportsOperation(account Account, operation string) bool {
	operations := account.Operations
	if len(operations) == 0 {
		operations = DefaultOperations(account.Type)
	}
	for _, candidate := range operations {
		if candidate == operation {
			return true
		}
	}
	return false
}

func normalizeStringList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
