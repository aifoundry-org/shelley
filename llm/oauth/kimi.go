package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Public Kimi Code OAuth client, using the international auth host.
// The device and token paths match MoonshotAI/kimi-cli's auth/oauth.py.
const (
	kimiClientID         = "17e5f671-d194-4dfb-9706-5516cb48c098"
	kimiDeviceEP         = "https://auth.kimi.ai/api/oauth/device_authorization"
	kimiTokenEP          = "https://auth.kimi.ai/api/oauth/token"
	kimiRequestTimeout   = 30 * time.Second
	kimiMaxResponseBytes = 1 << 20
)

// KimiDeviceAuth holds the user-facing details of a device-code login.
type KimiDeviceAuth struct {
	UserCode        string
	VerificationURL string
	ExpiresAt       time.Time

	mu          sync.Mutex
	deviceCode  string
	interval    time.Duration
	nextPoll    time.Time
	inFlight    bool
	done        bool
	terminalErr error
}

// KimiDeviceFlow authorizes a Kimi Code subscription without a local callback.
type KimiDeviceFlow struct {
	Store *Store
	HTTPC *http.Client

	deviceEP string
	tokenEP  string
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

// NewKimiDeviceFlow builds a device-code login backed by the given store.
func NewKimiDeviceFlow(store *Store, httpc *http.Client) *KimiDeviceFlow {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &KimiDeviceFlow{
		Store: store, HTTPC: httpc, deviceEP: kimiDeviceEP, tokenEP: kimiTokenEP,
		now: time.Now, wait: kimiWait,
	}
}

func kimiWait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Start requests a device/user code pair. The lifetime starts here, not when
// Poll is called, so time spent displaying the instructions counts toward expiry.
func (f *KimiDeviceFlow) Start(ctx context.Context) (*KimiDeviceAuth, error) {
	started := f.now()
	data, status, err := kimiRequest(ctx, f.HTTPC, f.deviceEP, url.Values{
		"client_id": {kimiClientID},
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("kimi device authorization: HTTP %d", status)
	}
	var response struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                *int64 `json:"interval"`
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, errors.New("kimi device authorization: invalid JSON response")
	}
	verificationURL := kimiVerificationURL(response.VerificationURI)
	if response.DeviceCode == "" || response.UserCode == "" || verificationURL == "" || !kimiValidSeconds(response.ExpiresIn) {
		return nil, errors.New("kimi device authorization: missing or invalid fields")
	}
	if response.VerificationURIComplete != "" {
		verificationURL = kimiVerificationURL(response.VerificationURIComplete)
		if verificationURL == "" {
			return nil, errors.New("kimi device authorization: invalid verification URL")
		}
	}
	interval := int64(5) // RFC 8628 default when interval is omitted.
	if response.Interval != nil {
		interval = *response.Interval
		if !kimiValidSeconds(interval) {
			return nil, errors.New("kimi device authorization: invalid interval")
		}
	}
	return &KimiDeviceAuth{
		UserCode: response.UserCode, VerificationURL: verificationURL,
		deviceCode: response.DeviceCode, interval: time.Duration(interval) * time.Second,
		ExpiresAt: started.Add(time.Duration(response.ExpiresIn) * time.Second),
		nextPoll:  f.now().Add(time.Duration(interval) * time.Second),
	}, nil
}

// Keep device verification on the international site even if the auth service
// returns a mainland link. Reject unknown authorities (including ports and
// userinfo) rather than exposing a user code to an untrusted verification site.
func kimiVerificationURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return ""
	}
	switch strings.ToLower(u.Host) {
	case "kimi.com", "kimi.ai":
		u.Host = "kimi.ai"
	case "www.kimi.com", "www.kimi.ai":
		u.Host = "www.kimi.ai"
	case "auth.kimi.com", "auth.kimi.ai":
		u.Host = "auth.kimi.ai"
	default:
		return ""
	}
	// URL.String preserves RawPath and RawQuery; never replace domain text in
	// paths, query values, or fragments.
	return u.String()
}

func kimiValidSeconds(seconds int64) bool {
	return seconds > 0 && seconds <= int64((1<<63-1)/time.Second)
}

// Poll waits for approval and saves the tokens under the "kimi" provider.
// It shares the device's polling schedule with TryPoll.
func (f *KimiDeviceFlow) Poll(ctx context.Context, device *KimiDeviceAuth) error {
	if device == nil {
		return errors.New("kimi device authorization: invalid device")
	}
	device.mu.Lock()
	if device.nextPoll.IsZero() {
		device.nextPoll = f.now().Add(device.interval)
	}
	device.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, device.ExpiresAt.Sub(f.now()))
	defer cancel()
	for {
		done, err := f.TryPoll(ctx, device)
		if err != nil || done {
			return err
		}
		device.mu.Lock()
		delay := device.nextPoll.Sub(f.now())
		if delay <= 0 {
			// Another caller may still have a request in flight.
			delay = device.interval
		}
		device.mu.Unlock()
		if err := f.wait(ctx, min(delay, device.ExpiresAt.Sub(f.now()))); err != nil {
			return err
		}
	}
}

// TryPoll makes at most one request, without waiting for the polling interval.
// Start schedules the first request after the provider's interval. Early or
// concurrent calls return false, nil. Completion is persisted and remembered, so a
// completed device never exchanges its code twice. Do not copy KimiDeviceAuth.
func (f *KimiDeviceFlow) TryPoll(ctx context.Context, device *KimiDeviceAuth) (done bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if device == nil {
		return false, errors.New("kimi device authorization: invalid device")
	}
	device.mu.Lock()
	if device.done || device.terminalErr != nil {
		done, err := device.done, device.terminalErr
		device.mu.Unlock()
		return done, err
	}
	if device.deviceCode == "" || device.interval <= 0 || device.ExpiresAt.IsZero() {
		device.mu.Unlock()
		return false, errors.New("kimi device authorization: invalid device")
	}
	remaining := device.ExpiresAt.Sub(f.now())
	if remaining <= 0 {
		device.mu.Unlock()
		return false, errors.New("kimi device authorization expired; restart login")
	}
	if device.inFlight || f.now().Before(device.nextPoll) {
		device.mu.Unlock()
		return false, nil
	}
	device.inFlight = true
	device.nextPoll = f.now().Add(device.interval)
	device.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	data, status, requestErr := kimiRequest(ctx, f.HTTPC, f.tokenEP, url.Values{
		"client_id":   {kimiClientID},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {device.deviceCode},
	})
	device.mu.Lock()
	defer func() {
		device.inFlight = false
		device.nextPoll = f.now().Add(device.interval)
		device.done, device.terminalErr = done, err
		device.mu.Unlock()
	}()
	if requestErr != nil {
		return false, requestErr
	}
	if !f.now().Before(device.ExpiresAt) {
		return false, errors.New("kimi device authorization expired; restart login")
	}
	if status >= 200 && status < 300 {
		tok, err := kimiParseToken(data, f.now())
		if err != nil {
			return false, err
		}
		if err := f.Store.Save("kimi", tok); err != nil {
			return false, err
		}
		return true, nil
	}
	var failure struct {
		Error    string `json:"error"`
		Interval int64  `json:"interval"`
	}
	if status >= 500 || json.Unmarshal(data, &failure) != nil {
		return false, fmt.Errorf("kimi device token request: HTTP %d", status)
	}
	switch failure.Error {
	case "authorization_pending":
		return false, nil
	case "slow_down":
		// Saturate to the remaining lifetime to avoid duration overflow.
		device.interval = min(device.interval, device.ExpiresAt.Sub(f.now()))
		if device.interval <= time.Duration(1<<63-1)-5*time.Second {
			device.interval += 5 * time.Second
		}
		if kimiValidSeconds(failure.Interval) {
			device.interval = max(device.interval, time.Duration(failure.Interval)*time.Second)
		}
		return false, nil
	case "expired_token":
		return false, errors.New("kimi device authorization expired; restart login")
	case "access_denied":
		return false, errors.New("kimi device authorization denied")
	default:
		return false, fmt.Errorf("kimi device token request: HTTP %d", status)
	}
}

// NewKimiTokenSource builds a TokenSource using Kimi's refresh endpoint.
func NewKimiTokenSource(store *Store, httpc *http.Client) *TokenSource {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &TokenSource{
		Provider: "kimi", Store: store,
		refresh: func(ctx context.Context, rt string) (Token, error) {
			data, status, err := kimiRequest(ctx, httpc, kimiTokenEP, url.Values{
				"client_id": {kimiClientID}, "grant_type": {"refresh_token"}, "refresh_token": {rt},
			})
			if err != nil {
				return Token{}, err
			}
			if status < 200 || status >= 300 {
				return Token{}, fmt.Errorf("kimi token refresh: HTTP %d; re-run login if authorization was revoked", status)
			}
			return kimiParseToken(data, time.Now())
		},
	}
}

func kimiParseToken(data []byte, now time.Time) (Token, error) {
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.Unmarshal(data, &response) != nil {
		return Token{}, errors.New("kimi token: invalid JSON response")
	}
	if response.AccessToken == "" || response.RefreshToken == "" || !kimiValidSeconds(response.ExpiresIn) {
		return Token{}, errors.New("kimi token: missing or invalid fields")
	}
	return Token{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken,
		ExpiresAt: now.Add(time.Duration(response.ExpiresIn) * time.Second)}, nil
}

// Bound time and response size even when the supplied client has no timeout.
// Never surface response bodies, arbitrary server errors, or transport errors:
// they can contain credentials. Preserve only context cancellation/deadlines.
func kimiRequest(ctx context.Context, httpc *http.Client, endpoint string, form url.Values) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, kimiRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, errors.New("kimi OAuth: invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// OAuth credentials must not be forwarded through redirects.
	client := *httpc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, errors.New("kimi OAuth request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, kimiMaxResponseBytes+1))
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	if err != nil {
		return nil, 0, errors.New("kimi OAuth: read response failed")
	}
	if len(data) > kimiMaxResponseBytes {
		return nil, 0, errors.New("kimi OAuth: response too large")
	}
	return data, resp.StatusCode, nil
}
