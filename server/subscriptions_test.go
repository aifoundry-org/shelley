package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

func TestHandleSubscriptionsReportsCredentialStatus(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	store := &oauth.Store{Path: credPath}
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, provider := range []string{"anthropic", "kimi"} {
		if err := store.Save(provider, oauth.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: expiresAt}); err != nil {
			t.Fatalf("save token: %v", err)
		}
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
	if len(got.Providers) != 3 {
		t.Fatalf("providers = %+v, want three providers", got.Providers)
	}
	for _, provider := range []string{"anthropic", "kimi"} {
		st := got.Providers[provider]
		if !st.LoggedIn || st.ExpiresAt != expiresAt.Format(time.RFC3339) || !strings.Contains(st.Status, "token valid until") {
			t.Fatalf("%s status = %+v", provider, st)
		}
	}
	if got.Providers["openai"].LoggedIn {
		t.Fatalf("openai status = %+v, want logged out", got.Providers["openai"])
	}
}

func TestHandleSubscriptionLogoutDeletesCredentialsAndRefreshesModels(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			credPath := filepath.Join(t.TempDir(), "credentials.json")
			store := &oauth.Store{Path: credPath}
			if err := store.Save(provider, oauth.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatalf("save token: %v", err)
			}
			if err := store.Save("other-provider", oauth.Token{AccessToken: "keep"}); err != nil {
				t.Fatal(err)
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

			req := httptest.NewRequest(http.MethodPost, "/api/subscriptions/"+provider+"/logout", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
			}
			if _, err := store.Load(provider); err == nil {
				t.Fatal("token still present")
			}
			if tok, err := store.Load("other-provider"); err != nil || tok.AccessToken != "keep" {
				t.Fatal("logout changed another provider's credentials")
			}
			if !refreshed {
				t.Fatal("model refresh was not called")
			}
			if !mgr.HasModel("new-built") || mgr.HasModel("old-built") {
				t.Fatalf("models were not refreshed")
			}
		})
	}
}

func TestValidSubscriptionProvider(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "kimi"} {
		if !validSubscriptionProvider(provider) {
			t.Errorf("provider %q rejected", provider)
		}
	}
	for _, provider := range []string{"", "Kimi", "kimi-coding", "unknown"} {
		if validSubscriptionProvider(provider) {
			t.Errorf("provider %q accepted", provider)
		}
	}
}

func TestHandleSubscriptionsLoggedOut(t *testing.T) {
	s := &Server{credentialsPath: filepath.Join(t.TempDir(), "credentials.json")}
	rec := httptest.NewRecorder()
	s.handleSubscriptions(rec, httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil))
	var got subscriptionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"anthropic", "openai", "kimi"} {
		st, ok := got.Providers[provider]
		if !ok || st.LoggedIn || st.Status != "not logged in" || st.ExpiresAt != "" {
			t.Errorf("%s status = %+v (present=%v)", provider, st, ok)
		}
	}
}

func TestSubscriptionLoginRejectsInvalidRequests(t *testing.T) {
	s := &Server{subscriptionSessions: map[string]subscriptionLoginSession{
		"openai":          {Provider: "openai", OpenAI: &oauth.OpenAIDeviceFlow{}, Device: &oauth.DeviceAuth{}},
		"kimi":            {Provider: "kimi", Kimi: &oauth.KimiDeviceFlow{}, KimiDevice: &oauth.KimiDeviceAuth{}},
		"incomplete-kimi": {Provider: "kimi"},
		"no-kimi-device":  {Provider: "kimi", Kimi: &oauth.KimiDeviceFlow{}},
		"no-kimi-flow":    {Provider: "kimi", KimiDevice: &oauth.KimiDeviceAuth{}},
	}}
	for _, tt := range []struct {
		name, provider, body string
		handler              http.HandlerFunc
		status               int
	}{
		{"unknown start", "unknown", "", s.handleSubscriptionLoginStart, http.StatusBadRequest},
		{"unknown logout", "unknown", "", s.handleSubscriptionLogout, http.StatusBadRequest},
		{"unknown poll", "unknown", "{}", s.handleSubscriptionLoginPoll, http.StatusBadRequest},
		{"anthropic poll", "anthropic", "{}", s.handleSubscriptionLoginPoll, http.StatusBadRequest},
		{"kimi malformed poll", "kimi", "{", s.handleSubscriptionLoginPoll, http.StatusBadRequest},
		{"openai malformed poll", "openai", "{", s.handleSubscriptionLoginPoll, http.StatusBadRequest},
		{"kimi missing session", "kimi", `{"session_id":"missing"}`, s.handleSubscriptionLoginPoll, http.StatusNotFound},
		{"kimi openai session", "kimi", `{"session_id":"openai"}`, s.handleSubscriptionLoginPoll, http.StatusNotFound},
		{"openai kimi session", "openai", `{"session_id":"kimi"}`, s.handleSubscriptionLoginPoll, http.StatusNotFound},
		{"kimi incomplete session", "kimi", `{"session_id":"incomplete-kimi"}`, s.handleSubscriptionLoginPoll, http.StatusNotFound},
		{"kimi missing device", "kimi", `{"session_id":"no-kimi-device"}`, s.handleSubscriptionLoginPoll, http.StatusNotFound},
		{"kimi missing flow", "kimi", `{"session_id":"no-kimi-flow"}`, s.handleSubscriptionLoginPoll, http.StatusNotFound},
		{"kimi complete", "kimi", `{"session_id":"kimi","code":"code"}`, s.handleSubscriptionLoginComplete, http.StatusBadRequest},
		{"openai complete", "openai", `{"session_id":"openai","code":"code"}`, s.handleSubscriptionLoginComplete, http.StatusBadRequest},
		{"unknown complete", "unknown", "{}", s.handleSubscriptionLoginComplete, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.SetPathValue("provider", tt.provider)
			rec := httptest.NewRecorder()
			tt.handler(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}

func TestKimiSubscriptionDeviceLogin(t *testing.T) {
	for _, tt := range []struct {
		name, pollBody             string
		upstreamStatus, wantStatus int
		done                       bool
	}{
		{"pending", `{"error":"authorization_pending"}`, http.StatusBadRequest, http.StatusOK, false},
		{"approved", `{"access_token":"secret-access","refresh_token":"secret-refresh","expires_in":3600}`, http.StatusOK, http.StatusOK, true},
		{"denied", `{"error":"access_denied"}`, http.StatusBadRequest, http.StatusBadGateway, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				credPath := filepath.Join(t.TempDir(), "credentials.json")
				requests := 0
				client := http.DefaultClient
				http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Host != "auth.kimi.ai" {
						t.Fatalf("OAuth host = %q, want international host", req.URL.Host)
					}
					requests++
					status := http.StatusOK
					body := `{"device_code":"secret-device","user_code":"ABCD","verification_uri":"https://auth.kimi.com/device","expires_in":1800,"interval":5}`
					switch req.URL.Path {
					case "/api/oauth/device_authorization":
					case "/api/oauth/token":
						status, body = tt.upstreamStatus, tt.pollBody
					default:
						t.Fatalf("unexpected OAuth request: %s", req.URL.Path)
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				t.Cleanup(func() { http.DefaultClient = client })
				mgr, err := models.NewManager(&models.Config{
					Models: []models.Built{{ID: "old-built", Provider: models.ProviderBuiltIn, Service: predictable.NewService()}},
					Logger: slog.Default(),
				})
				if err != nil {
					t.Fatal(err)
				}
				refreshed := false
				s := &Server{
					credentialsPath: credPath, subscriptionSessions: map[string]subscriptionLoginSession{},
					llmManager: mgr, logger: slog.Default(),
					refreshBuiltModels: func(context.Context) ([]models.Built, error) {
						refreshed = true
						return []models.Built{{ID: "new-built", Provider: models.ProviderBuiltIn, Service: predictable.NewService()}}, nil
					},
				}
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.SetPathValue("provider", "kimi")
				rec := httptest.NewRecorder()
				s.handleSubscriptionLoginStart(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("start: status=%d body=%s", rec.Code, rec.Body.String())
				}
				var start map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
					t.Fatal(err)
				}
				if len(start) != 3 || start["session_id"] == "" || start["user_code"] != "ABCD" || start["verification_url"] != "https://auth.kimi.ai/device" {
					t.Fatalf("unexpected login response: %v", start)
				}
				sess := s.subscriptionSessions[start["session_id"]]
				if sess.Provider != "kimi" || sess.Kimi == nil || sess.KimiDevice == nil || sess.OpenAI != nil || sess.Claude != nil {
					t.Fatal("incorrect login session")
				}
				poll := func(wantStatus int, done bool) {
					t.Helper()
					req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"session_id":"`+start["session_id"]+`"}`))
					req.SetPathValue("provider", "kimi")
					rec := httptest.NewRecorder()
					s.handleSubscriptionLoginPoll(rec, req)
					if rec.Code != wantStatus {
						t.Fatalf("poll: status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
					}
					if strings.Contains(rec.Body.String(), "secret-") {
						t.Fatal("poll exposed credentials")
					}
					if wantStatus == http.StatusOK {
						var result map[string]bool
						if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						if got, ok := result["done"]; !ok || got != done {
							t.Fatalf("poll result=%v, want done=%v", result, done)
						}
					}
				}
				poll(http.StatusOK, false)
				poll(http.StatusOK, false)
				if requests != 1 || refreshed {
					t.Fatalf("requests=%d refreshed=%v before initial interval", requests, refreshed)
				}
				// synctest advances virtual time when this timer channel blocks;
				// no wall-clock sleep or relaxation of the provider interval.
				<-time.After(5 * time.Second)
				poll(tt.wantStatus, tt.done)
				if requests != 2 || refreshed != tt.done {
					t.Fatalf("requests=%d refreshed=%v done=%v", requests, refreshed, tt.done)
				}
				_, remains := s.subscriptionSessions[start["session_id"]]
				if remains == tt.done {
					t.Fatalf("session remains=%v after done=%v", remains, tt.done)
				}
				store := &oauth.Store{Path: credPath}
				tok, err := store.Load("kimi")
				if tt.done {
					if err != nil || tok.AccessToken != "secret-access" || tok.RefreshToken != "secret-refresh" {
						t.Fatalf("credentials not saved correctly: %v", err)
					}
					if !mgr.HasModel("new-built") || mgr.HasModel("old-built") {
						t.Fatal("model list not refreshed")
					}
					poll(http.StatusNotFound, false)
				} else if err == nil {
					t.Fatal("credentials saved before approval")
				}
			})
		})
	}
}

func TestKimiSubscriptionStartFailure(t *testing.T) {
	client := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("upstream failure"))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = client })
	s := &Server{credentialsPath: filepath.Join(t.TempDir(), "credentials.json"), subscriptionSessions: map[string]subscriptionLoginSession{}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("provider", "kimi")
	rec := httptest.NewRecorder()
	s.handleSubscriptionLoginStart(rec, req)
	if rec.Code != http.StatusBadGateway || len(s.subscriptionSessions) != 0 {
		t.Fatalf("failed start: status=%d sessions=%d", rec.Code, len(s.subscriptionSessions))
	}
}

func TestKimiSubscriptionExpiredStatus(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	store := &oauth.Store{Path: credPath}
	expiresAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := store.Save("kimi", oauth.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: expiresAt}); err != nil {
		t.Fatal(err)
	}
	s := &Server{credentialsPath: credPath}
	rec := httptest.NewRecorder()
	s.handleSubscriptions(rec, httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil))
	var got subscriptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	st := got.Providers["kimi"]
	if !st.LoggedIn || st.ExpiresAt != expiresAt.Format(time.RFC3339) || !strings.Contains(st.Status, "will refresh on next use") {
		t.Fatalf("expired Kimi status = %+v", st)
	}
}
