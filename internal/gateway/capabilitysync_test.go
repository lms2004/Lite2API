package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestDecodeDiscoveredModelIDsAcceptsCommonShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"openai", `{"data":[{"id":"gpt-5.6-sol"},{"id":"fast"}]}`, []string{"gpt-5.6-sol", "fast"}},
		{"models", `{"models":["a",{"id":"b"},"a"]}`, []string{"a", "b"}},
		{"direct", `["x",{"id":"y"}]`, []string{"x", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeDiscoveredModelIDs([]byte(tc.body)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscoverModelsForAccountUsesAccountAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization=%q", got)
		}
		if got := r.Header.Get("X-Test"); got != "present" {
			t.Fatalf("custom header=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-sol"},{"id":"gpt-5.6-terra"}]}`))
	}))
	defer server.Close()

	account := config.Account{
		ID: "test", Type: "openai", BaseURL: server.URL + "/v1",
		APIKey: "secret", AuthHeader: "authorization", AuthScheme: "Bearer",
		Headers: map[string]string{"X-Test": "present"}, Enabled: true,
	}
	models, err := discoverModelsForAccount(context.Background(), server.Client(), account)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"gpt-5.6-sol", "gpt-5.6-terra"}) {
		t.Fatalf("models=%v", models)
	}
}

func TestMergeSyncedCapabilitiesEnrichesExistingReasoning(t *testing.T) {
	existing := []config.ChannelCapability{{
		Model: "sol", UpstreamModel: "gpt-5.6-sol", ReasoningEfforts: []string{"auto", "high"},
	}}
	inferred := []config.ChannelCapability{{
		Model: "sol", UpstreamModel: "gpt-5.6-sol", ReasoningEfforts: []string{"auto", "none", "low", "medium", "high", "xhigh", "max"},
	}}
	got := mergeSyncedCapabilities(existing, inferred)
	if len(got) != 1 {
		t.Fatalf("capabilities=%+v", got)
	}
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		found := false
		for _, value := range got[0].ReasoningEfforts {
			if value == effort {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing effort %q in %v", effort, got[0].ReasoningEfforts)
		}
	}
}
