package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webassets "github.com/lms2004/lite2api/internal/web"
)

func TestSecurityHeadersPinCanonicalAdminScript(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin", nil))

	policy := recorder.Header().Get("Content-Security-Policy")
	if strings.Contains(policy, "script-src 'unsafe-inline'") {
		t.Fatalf("canonical admin script must not require unsafe-inline: %s", policy)
	}
	if !strings.Contains(policy, "script-src "+webassets.ScriptCSPSource) {
		t.Fatalf("canonical script hash is missing from CSP: %s", policy)
	}
	for _, directive := range []string{"img-src 'self' data:", "object-src 'none'", "form-action 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(policy, directive) {
			t.Fatalf("CSP is missing %q: %s", directive, policy)
		}
	}
}
