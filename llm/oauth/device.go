package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenAI device-code ("Sign in with Device Code") endpoints. This flow needs no
// localhost callback: the CLI shows a URL and a short code, the user approves
// in any browser, and the CLI polls for completion.
const (
	openAIAccountsBaseURL   = "https://auth.openai.com/api/accounts"
	openAIDeviceVerifyURL   = "https://auth.openai.com/codex/device"
	openAIDeviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
)

// DeviceAuth holds the user-facing details of an in-progress device-code login.
type DeviceAuth struct {
	UserCode        string // short code the user types in the browser
	VerificationURL string // URL the user opens to enter the code

	deviceAuthID string
	interval     time.Duration
}

// OpenAIDeviceFlow drives the Codex device-code login: request a user code,
// poll until the user approves, then exchange for tokens and persist them.
type OpenAIDeviceFlow struct {
	Store *Store
	HTTPC *http.Client

	apiBaseURL string                          // overridable for tests
	tokenEP    string                          // overridable for tests
	wait       func(ctx context.Context) error // sleep between polls; overridable for tests
	interval   time.Duration
}

// NewOpenAIDeviceFlow builds a device-code login flow backed by the given store.
func NewOpenAIDeviceFlow(store *Store, httpc *http.Client) *OpenAIDeviceFlow {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	f := &OpenAIDeviceFlow{
		Store:      store,
		HTTPC:      httpc,
		apiBaseURL: openAIAccountsBaseURL,
		tokenEP:    openAITokenEP,
		interval:   5 * time.Second,
	}
	f.wait = func(ctx context.Context) error {
		select {
		case <-time.After(f.interval):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f
}

// userCodeResponse is the /deviceauth/usercode reply. The user-code field is
// sometimes "user_code" and sometimes "usercode"; accept both.
type userCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
}

func (r userCodeResponse) userCode() string {
	if r.UserCode != "" {
		return r.UserCode
	}
	return r.UserCodeAlt
}

// Start requests a device/user code pair from the backend.
func (f *OpenAIDeviceFlow) Start(ctx context.Context) (*DeviceAuth, error) {
	payload, _ := json.Marshal(map[string]string{"client_id": openAIClientID})
	req, err := http.NewRequestWithContext(ctx, "POST", f.apiBaseURL+"/deviceauth/usercode", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTPC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("device code login is not enabled for this account; enable it at https://chatgpt.com/settings/security")
		}
		return nil, fmt.Errorf("device usercode request: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var ucr userCodeResponse
	if err := json.Unmarshal(data, &ucr); err != nil {
		return nil, fmt.Errorf("device usercode decode: %w", err)
	}
	if ucr.userCode() == "" || ucr.DeviceAuthID == "" {
		return nil, fmt.Errorf("device usercode response missing code: %s", strings.TrimSpace(string(data)))
	}
	if iv := parseInterval(ucr.Interval); iv > 0 {
		f.interval = iv
	}
	return &DeviceAuth{
		UserCode:        ucr.userCode(),
		VerificationURL: openAIDeviceVerifyURL,
		deviceAuthID:    ucr.DeviceAuthID,
		interval:        f.interval,
	}, nil
}

// parseInterval accepts either a JSON number or a quoted string.
func parseInterval(raw json.RawMessage) time.Duration {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return time.Duration(n) * time.Second
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if iv, err := time.ParseDuration(s + "s"); err == nil {
			return iv
		}
	}
	return 0
}

// deviceCodeResponse is the /deviceauth/token reply once the user approves. The
// backend generates the PKCE pair and returns the verifier here.
type deviceCodeResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// Poll waits for the user to approve the device code, then exchanges the
// returned authorization code for tokens and persists them to the store.
func (f *OpenAIDeviceFlow) Poll(ctx context.Context, da *DeviceAuth) error {
	for {
		if err := f.wait(ctx); err != nil {
			return err
		}
		code, done, err := f.pollOnce(ctx, da)
		if err != nil {
			return err
		}
		if !done {
			continue
		}
		tok, err := f.exchange(ctx, code)
		if err != nil {
			return err
		}
		return f.Store.Save("openai", tok)
	}
}

// pollOnce makes one poll. done=false means "keep polling" (still pending).
func (f *OpenAIDeviceFlow) pollOnce(ctx context.Context, da *DeviceAuth) (deviceCodeResponse, bool, error) {
	payload, _ := json.Marshal(map[string]string{
		"device_auth_id": da.deviceAuthID,
		"user_code":      da.UserCode,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", f.apiBaseURL+"/deviceauth/token", bytes.NewReader(payload))
	if err != nil {
		return deviceCodeResponse{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTPC.Do(req)
	if err != nil {
		return deviceCodeResponse{}, false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var dcr deviceCodeResponse
		if err := json.Unmarshal(data, &dcr); err != nil {
			return deviceCodeResponse{}, false, fmt.Errorf("device token decode: %w", err)
		}
		return dcr, true, nil
	case http.StatusForbidden, http.StatusNotFound:
		return deviceCodeResponse{}, false, nil // still pending
	default:
		return deviceCodeResponse{}, false, fmt.Errorf("device token poll: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
}

// exchange swaps the device authorization code for OAuth tokens.
func (f *OpenAIDeviceFlow) exchange(ctx context.Context, code deviceCodeResponse) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openAIClientID)
	form.Set("code", code.AuthorizationCode)
	form.Set("code_verifier", code.CodeVerifier)
	form.Set("redirect_uri", openAIDeviceRedirectURI)
	req, err := http.NewRequestWithContext(ctx, "POST", f.tokenEP, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.HTTPC.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("device token exchange: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var tr openAITokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return Token{}, fmt.Errorf("device token exchange decode: %w", err)
	}
	return Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		AccountID:    accountIDFromIDToken(tr.IDToken),
	}, nil
}
