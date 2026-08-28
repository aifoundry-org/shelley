package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

func TestHandleSubscriptionsReportsCredentialStatus(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	store := &oauth.Store{Path: credPath}
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := store.Save("anthropic", oauth.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("save token: %v", err)
	}
	s := &Server{credentialsPath: credPath}

	req := httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil)
	rec := httptest.NewRecorder()
	s.handleSubscriptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var got subscriptionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.CredentialsPath != credPath {
		t.Fatalf("credentials_path = %q, want %q", got.CredentialsPath, credPath)
	}
	if !got.Providers["anthropic"].LoggedIn || got.Providers["anthropic"].ExpiresAt != expiresAt.Format(time.RFC3339) {
		t.Fatalf("anthropic status = %+v", got.Providers["anthropic"])
	}
	if got.Providers["openai"].LoggedIn {
		t.Fatalf("openai status = %+v, want logged out", got.Providers["openai"])
	}
}

func TestHandleSubscriptionLogoutDeletesCredentialsAndRefreshesModels(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	store := &oauth.Store{Path: credPath}
	if err := store.Save("openai", oauth.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("save token: %v", err)
	}
	mgr, err := models.NewManager(&models.Config{
		Models: []models.Built{{ID: "old-built", Provider: models.ProviderBuiltIn, Service: predictable.NewService()}},
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	refreshed := false
	s := &Server{
		llmManager:      mgr,
		logger:          slog.Default(),
		credentialsPath: credPath,
		refreshBuiltModels: func(context.Context) ([]models.Built, error) {
			refreshed = true
			return []models.Built{{ID: "new-built", Provider: models.ProviderBuiltIn, Service: predictable.NewService()}}, nil
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/subscriptions/{provider}/logout", s.handleSubscriptionLogout)

	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions/openai/logout", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if _, err := store.Load("openai"); err == nil {
		t.Fatal("openai token still present")
	}
	if !refreshed {
		t.Fatal("model refresh was not called")
	}
	if !mgr.HasModel("new-built") || mgr.HasModel("old-built") {
		t.Fatalf("models were not refreshed")
	}
}
