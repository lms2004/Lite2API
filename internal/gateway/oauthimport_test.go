package gateway

import (
	"errors"
	"testing"
)

func TestIsOAuthImportItem(t *testing.T) {
	cases := []struct {
		name string
		item AccountImportItem
		want bool
	}{
		{"api_key subset", AccountImportItem{Platform: "deepseek", Type: "api_key", Credentials: map[string]any{"api_key": "secret"}}, false},
		{"explicit oauth type", AccountImportItem{Platform: "openai", Type: "oauth", Credentials: map[string]any{"access_token": "tok"}}, true},
		{"tokens without type", AccountImportItem{Platform: "anthropic", Credentials: map[string]any{"refresh_token": "rt"}}, true},
		{"api_key wins over tokens", AccountImportItem{Type: "oauth", Credentials: map[string]any{"api_key": "k", "access_token": "tok"}}, false},
		{"plain openai channel", AccountImportItem{Type: "openai", BaseURL: "https://api.example.com/v1"}, false},
	}
	for _, tc := range cases {
		if got := tc.item.isOAuthImportItem(); got != tc.want {
			t.Errorf("%s: isOAuthImportItem=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildOAuthAuthFileCodex(t *testing.T) {
	// Mirrors the real Sub2API OpenAI OAuth export shape.
	item := AccountImportItem{
		Name: "RachelHall46948K@outlook.com", Platform: "openai", Type: "oauth",
		Credentials: map[string]any{
			"access_token":    "at",
			"refresh_token":   "rt",
			"id_token":        "it",
			"email":           "RachelHall46948K@outlook.com",
			"expires_at":      "2026-08-28T11:45:22.000Z",
			"chatgpt_user_id": "user-x",
		},
	}
	provider, name, bundle, err := item.buildOAuthAuthFile(0)
	if err != nil {
		t.Fatal(err)
	}
	if provider != "codex" {
		t.Fatalf("provider=%q", provider)
	}
	if name != "codex-rachelhall46948k@outlook.com.json" {
		t.Fatalf("name=%q", name)
	}
	if bundle["type"] != "codex" || bundle["access_token"] != "at" || bundle["refresh_token"] != "rt" || bundle["id_token"] != "it" {
		t.Fatalf("bundle=%+v", bundle)
	}
	if bundle["email"] != "RachelHall46948K@outlook.com" {
		t.Fatalf("email=%v", bundle["email"])
	}
	if bundle["expired"] != "2026-08-28T11:45:22Z" {
		t.Fatalf("expired=%v", bundle["expired"])
	}
	if _, ok := bundle["last_refresh"].(string); !ok || bundle["last_refresh"] == "" {
		t.Fatalf("last_refresh=%v", bundle["last_refresh"])
	}
}

func TestBuildOAuthAuthFilePlatforms(t *testing.T) {
	base := map[string]any{"access_token": "at", "refresh_token": "rt", "email": "a@b.com", "expires_at": float64(1787917522)}
	for _, tc := range []struct{ platform, provider, wantType, wantName string }{
		{"claude", "claude", "claude", "claude-a@b.com.json"},
		{"anthropic", "claude", "claude", "claude-a@b.com.json"},
		{"antigravity", "antigravity", "antigravity", "antigravity-a@b.com.json"},
	} {
		item := AccountImportItem{Platform: tc.platform, Type: "oauth", Credentials: base}
		provider, name, bundle, err := item.buildOAuthAuthFile(0)
		if err != nil {
			t.Fatalf("%s: %v", tc.platform, err)
		}
		if provider != tc.provider || name != tc.wantName || bundle["type"] != tc.wantType {
			t.Fatalf("%s: provider=%q name=%q type=%v", tc.platform, provider, name, bundle["type"])
		}
	}
}

func TestBuildOAuthAuthFileGemini(t *testing.T) {
	item := AccountImportItem{Platform: "gemini", Type: "oauth", Credentials: map[string]any{
		"access_token": "at", "refresh_token": "rt", "email": "g@b.com", "project_id": "proj-1",
	}}
	provider, name, bundle, err := item.buildOAuthAuthFile(2)
	if err != nil {
		t.Fatal(err)
	}
	if provider != "gemini" || name != "gemini-g@b.com-proj-1.json" || bundle["type"] != "gemini" {
		t.Fatalf("provider=%q name=%q bundle=%+v", provider, name, bundle)
	}
	token, ok := bundle["token"].(map[string]any)
	if !ok || token["access_token"] != "at" || token["token_type"] != "Bearer" {
		t.Fatalf("token=%+v", bundle["token"])
	}
}

func TestBuildOAuthAuthFileUnpooledPlatform(t *testing.T) {
	item := AccountImportItem{Platform: "grok", Type: "oauth", Credentials: map[string]any{"access_token": "at"}}
	if _, _, _, err := item.buildOAuthAuthFile(0); !errors.Is(err, errOAuthPlatformNotPooled) {
		t.Fatalf("err=%v", err)
	}
}

func TestNormalizeOAuthExpiry(t *testing.T) {
	if got := normalizeOAuthExpiry("2026-08-28T11:45:22.000Z"); got != "2026-08-28T11:45:22Z" {
		t.Fatalf("iso: %q", got)
	}
	if got := normalizeOAuthExpiry(float64(1787917522)); got != "2026-08-28T11:45:22Z" {
		t.Fatalf("seconds: %q", got)
	}
	if got := normalizeOAuthExpiry(float64(1787917522000)); got != "2026-08-28T11:45:22Z" {
		t.Fatalf("millis: %q", got)
	}
	if got := normalizeOAuthExpiry(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestSafeOAuthName(t *testing.T) {
	if got := safeOAuthName("RachelHall46948K@outlook.com"); got != "rachelhall46948k@outlook.com" {
		t.Fatalf("got %q", got)
	}
	if got := safeOAuthName("  weird//name!!  "); got != "weird-name" {
		t.Fatalf("got %q", got)
	}
	if got := safeOAuthName(""); got != "account" {
		t.Fatalf("got %q", got)
	}
}
