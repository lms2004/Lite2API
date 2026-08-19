package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestParseModelIDs(t *testing.T) {
	body := []byte(`{
		"data": [
			{"id": "model-z"},
			{"id": "model-a"},
			{"id": "model-a"}
		],
		"models": [
			{"name": "model-b"},
			{"id": "model-c"}
		]
	}`)
	got := parseModelIDs(body)
	want := []string{"model-a", "model-b", "model-c", "model-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModelIDs() = %#v, want %#v", got, want)
	}
}

func TestProbeAccountModelsUsesConfiguredAuthentication(t *testing.T) {
	var observedPath string
	var observedAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedPath = r.URL.Path
		observedAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]string{
				{"id": "fast-model"},
				{"id": "quality-model"},
			},
		})
	}))
	defer server.Close()

	result, err := probeAccountModels(context.Background(), config.Account{
		ID:         "preview",
		Name:       "Preview",
		Type:       "openai",
		BaseURL:    server.URL + "/v1",
		APIKey:     "secret-value",
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
	})
	if err != nil {
		t.Fatalf("probeAccountModels() error = %v", err)
	}
	if !result.OK || result.Status != http.StatusOK {
		t.Fatalf("unexpected result: %#v", result)
	}
	if observedPath != "/v1/models" {
		t.Fatalf("request path = %q, want /v1/models", observedPath)
	}
	if observedAuthorization != "Bearer secret-value" {
		t.Fatalf("Authorization = %q", observedAuthorization)
	}
	if result.ModelCount != 2 || !reflect.DeepEqual(result.Models, []string{"fast-model", "quality-model"}) {
		t.Fatalf("unexpected model catalog: %#v", result)
	}
}

func TestProbeAccountModelsReportsUpstreamStatusWithoutSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	result, err := probeAccountModels(context.Background(), config.Account{
		ID:         "preview",
		Name:       "Preview",
		Type:       "openai",
		BaseURL:    server.URL + "/v1",
		APIKey:     "never-return-this",
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
	})
	if err != nil {
		t.Fatalf("probeAccountModels() error = %v", err)
	}
	if result.OK || result.Status != http.StatusUnauthorized {
		t.Fatalf("unexpected result: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "never-return-this") {
		t.Fatalf("test result leaked a credential: %s", encoded)
	}
}
