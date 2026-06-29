package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// deviceTestServer is a fake OpenAI device-auth backend. pollsUntilOK controls
// how many /deviceauth/token calls return "pending" before success.
type deviceTestServer struct {
	pollsUntilOK int32
	pollCount    int32
	userCodeBody map[string]any
}

func (d *deviceTestServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		if body["client_id"] != openAIClientID {
			t.Errorf("usercode client_id = %v", body["client_id"])
		}
		json.NewEncoder(w).Encode(d.userCodeBody)
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&d.pollCount, 1)
		if n <= d.pollsUntilOK {
			w.WriteHeader(http.StatusForbidden) // pending
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"authorization_code": "auth-code-xyz",
			"code_verifier":      "server-verifier",
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "auth-code-xyz" {
			t.Errorf("code = %q", r.Form.Get("code"))
		}
		if r.Form.Get("code_verifier") != "server-verifier" {
			t.Errorf("code_verifier = %q", r.Form.Get("code_verifier"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc",
			"refresh_token": "ref",
			"expires_in":    3600,
			"id_token":      makeIDToken("acct-dev"),
		})
	})
	return mux
}

func newDeviceFlow(t *testing.T, srv *httptest.Server) (*OpenAIDeviceFlow, *Store) {
	t.Helper()
	store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	flow := NewOpenAIDeviceFlow(store, srv.Client())
	flow.apiBaseURL = srv.URL + "/api/accounts"
	flow.tokenEP = srv.URL + "/oauth/token"
	flow.wait = func(context.Context) error { return nil } // no sleeps in tests
	return flow, store
}

func TestDeviceFlowStartReturnsUserCode(t *testing.T) {
	backend := &deviceTestServer{userCodeBody: map[string]any{
		"device_auth_id": "dev-1",
		"user_code":      "ABCD-1234",
		"interval":       5,
	}}
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	flow, _ := newDeviceFlow(t, srv)
	da, err := flow.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if da.UserCode != "ABCD-1234" {
		t.Errorf("UserCode = %q", da.UserCode)
	}
	if da.VerificationURL == "" {
		t.Error("VerificationURL empty")
	}
}

func TestDeviceFlowPollsThenSucceeds(t *testing.T) {
	backend := &deviceTestServer{
		pollsUntilOK: 2,
		userCodeBody: map[string]any{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": 1},
	}
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	flow, store := newDeviceFlow(t, srv)
	da, err := flow.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := flow.Poll(context.Background(), da); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := atomic.LoadInt32(&backend.pollCount); got != 3 {
		t.Errorf("poll count = %d, want 3 (2 pending + 1 success)", got)
	}
	tok, err := store.Load("openai")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tok.AccessToken != "acc" || tok.AccountID != "acct-dev" {
		t.Errorf("stored token = %+v", tok)
	}
}

func TestDeviceFlowHandlesUsercodeAltField(t *testing.T) {
	// Some responses use "usercode" instead of "user_code".
	backend := &deviceTestServer{userCodeBody: map[string]any{
		"device_auth_id": "dev-1",
		"usercode":       "WXYZ-9999",
		"interval":       1,
	}}
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	flow, _ := newDeviceFlow(t, srv)
	da, err := flow.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if da.UserCode != "WXYZ-9999" {
		t.Errorf("UserCode = %q, want WXYZ-9999", da.UserCode)
	}
}

func TestDeviceFlowPollRespectsContextCancel(t *testing.T) {
	backend := &deviceTestServer{
		pollsUntilOK: 1 << 30, // never succeeds
		userCodeBody: map[string]any{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": 1},
	}
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	flow, _ := newDeviceFlow(t, srv)
	da, err := flow.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// wait cancels the context after the first pending poll.
	flow.wait = func(context.Context) error { cancel(); return ctx.Err() }
	if err := flow.Poll(ctx, da); err == nil {
		t.Fatal("expected error when context cancelled")
	}
}
