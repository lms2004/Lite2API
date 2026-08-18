package gateway

import (
	"encoding/json"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestApplyExecutionProfileToBodyInjectsFastTier(t *testing.T) {
	routes := map[string]config.Route{
		"coding-fast": {Model: "sol@fast"},
		"coding":      {Model: "sol"},
	}
	fastBody := applyExecutionProfileToBody([]byte(`{"model":"coding-fast","messages":[]}`), routes)
	var fast map[string]any
	if err := json.Unmarshal(fastBody, &fast); err != nil {
		t.Fatal(err)
	}
	if fast["service_tier"] != "priority" {
		t.Fatalf("Fast route service_tier=%v body=%s", fast["service_tier"], fastBody)
	}
	if fast["model"] != "coding-fast" {
		t.Fatalf("execution profile must not rewrite client route alias: %v", fast["model"])
	}

	standardBody := applyExecutionProfileToBody([]byte(`{"model":"coding","messages":[]}`), routes)
	var standard map[string]any
	if err := json.Unmarshal(standardBody, &standard); err != nil {
		t.Fatal(err)
	}
	if _, exists := standard["service_tier"]; exists {
		t.Fatalf("standard route unexpectedly received service_tier: %s", standardBody)
	}
}

func TestApplyExecutionProfileToBodySupportsDirectFastModel(t *testing.T) {
	body := applyExecutionProfileToBody([]byte(`{"model":"terra@fast","input":"hello"}`), nil)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["service_tier"] != "priority" {
		t.Fatalf("direct Fast profile body=%s", body)
	}
}

func TestApplyExecutionProfilePreservesClientTierForStandardRoute(t *testing.T) {
	body := applyExecutionProfileToBody([]byte(`{"model":"sol","service_tier":"flex","messages":[]}`), nil)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["service_tier"] != "flex" {
		t.Fatalf("standard route should preserve client service tier: %s", body)
	}
}
