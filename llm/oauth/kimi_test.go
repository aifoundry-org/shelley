package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type kimiTransport func(*http.Request) (*http.Response, error)

func (f kimiTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func kimiResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const kimiDeviceJSON = `{"device_code":"secret-device","user_code":"USER-CODE","verification_uri":"https://auth.kimi.com/verify","verification_uri_complete":"https://auth.kimi.com/verify?code=USER-CODE","expires_in":900,"interval":2}`
const kimiTokenJSON = `{"access_token":"secret-access","refresh_token":"secret-refresh","expires_in":3600}`

func kimiTestFlow(t *testing.T, transport kimiTransport) *KimiDeviceFlow {
	t.Helper()
	flow := NewKimiDeviceFlow(&Store{Path: filepath.Join(t.TempDir(), "credentials.json")}, &http.Client{Transport: transport})
	now := time.Now()
	flow.now = func() time.Time { return now }
	flow.wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	return flow
}

func kimiCheckRequest(t *testing.T, r *http.Request, endpoint string, expected url.Values) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.String() != endpoint {
		t.Errorf("request = %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", r.Header)
	}
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.PostForm, expected) {
		t.Errorf("form = %v, want %v", r.PostForm, expected)
	}
	deadline, ok := r.Context().Deadline()
	if !ok || time.Until(deadline) > kimiRequestTimeout {
		t.Error("request lacks bounded deadline")
	}
}

func TestKimiInternationalEndpoints(t *testing.T) {
	flow := NewKimiDeviceFlow(nil, nil)
	if flow.deviceEP != "https://auth.kimi.ai/api/oauth/device_authorization" {
		t.Fatalf("device endpoint = %q", flow.deviceEP)
	}
	if kimiTokenEP != "https://auth.kimi.ai/api/oauth/token" {
		t.Fatalf("refresh endpoint = %q", kimiTokenEP)
	}
	if flow.tokenEP != kimiTokenEP {
		t.Fatalf("token endpoint = %q", flow.tokenEP)
	}
}

func TestKimiVerificationURL(t *testing.T) {
	// Keep escapes, repeated parameters, ordering, and domain text in values intact.
	const suffix = "/code/%61uthorize_device/kimi.com?user_code=A%2fb%2BC&next=https%3A%2F%2Fwww.kimi.com&x=1&x=2+3#auth.kimi.com"
	for _, host := range []string{"kimi", "www.kimi", "auth.kimi"} {
		for _, domain := range []string{".com", ".ai"} {
			for _, authority := range []string{host + domain, strings.ToUpper(host + domain)} {
				raw := "https://" + authority + suffix
				t.Run(authority, func(t *testing.T) {
					if got, want := kimiVerificationURL(raw), "https://"+host+".ai"+suffix; got != want {
						t.Fatalf("verification URL = %q, want %q", got, want)
					}
				})
			}
		}
	}
	for _, raw := range []string{
		"", "/code/authorize_device", "//www.kimi.com/verify",
		"http://www.kimi.com/verify", "javascript:alert(1)",
		"https://example.com/verify?next=https://www.kimi.com",
		"https://www.kimi.com.evil.example/verify", "https://evil.kimi.com/verify",
		"https://www.kimi.ai.evil.example/verify", "https://evil.kimi.ai/verify",
		"https://www.kimi.com@evil.example/verify", "https://evil.example@www.kimi.com/verify",
		"https://www.kimi.com:8443/verify", "https://www.kimi.ai:443/verify",
		"https://www.kimi.com./verify", "https://www.kimi%2ecom/verify",
		"https://www.kimi.com\\@evil.example/verify", "https://www.kimi.com/%zz",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := kimiVerificationURL(raw); got != "" {
				t.Fatalf("accepted unsafe verification URL: %q", got)
			}
		})
	}
}

func TestKimiDeviceFlow(t *testing.T) {
	polls := 0
	flow := kimiTestFlow(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == kimiDeviceEP {
			kimiCheckRequest(t, r, kimiDeviceEP, url.Values{"client_id": {kimiClientID}})
			return kimiResponse(200, kimiDeviceJSON), nil
		}
		kimiCheckRequest(t, r, kimiTokenEP, url.Values{"client_id": {kimiClientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {"secret-device"}})
		polls++
		switch polls {
		case 1:
			return kimiResponse(400, `{"error":"authorization_pending"}`), nil
		case 2:
			return kimiResponse(400, `{"error":"slow_down"}`), nil
		case 3:
			return kimiResponse(429, `{"error":"slow_down","interval":20}`), nil
		case 4:
			return kimiResponse(400, `{"error":"authorization_pending"}`), nil
		default:
			return kimiResponse(200, kimiTokenJSON), nil
		}
	})
	var waits []time.Duration
	wait := flow.wait
	flow.wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return wait(ctx, d) }
	device, err := flow.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.UserCode != "USER-CODE" || device.VerificationURL != "https://auth.kimi.ai/verify?code=USER-CODE" {
		t.Fatalf("device = %+v", device)
	}
	other := Token{AccessToken: "other", ExpiresAt: flow.now().Add(time.Hour)}
	if err := flow.Store.Save("anthropic", other); err != nil {
		t.Fatal(err)
	}
	if err := flow.Poll(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	wantWaits := []time.Duration{2 * time.Second, 2 * time.Second, 7 * time.Second, 20 * time.Second, 20 * time.Second}
	if !reflect.DeepEqual(waits, wantWaits) {
		t.Errorf("waits = %v, want %v", waits, wantWaits)
	}
	token, err := flow.Store.Load("kimi")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "secret-access" || token.RefreshToken != "secret-refresh" || !token.ExpiresAt.Equal(flow.now().Add(time.Hour)) {
		t.Fatalf("token = %+v", token)
	}
	preserved, err := flow.Store.Load("anthropic")
	if err != nil || preserved.AccessToken != other.AccessToken {
		t.Fatalf("other credentials lost: %v", err)
	}
	info, err := os.Stat(flow.Store.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file mode: %v, %v", info, err)
	}
}

func TestKimiDeviceStartValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"complete", kimiDeviceJSON, true},
		{"defaults", `{"device_code":"d","user_code":"u","verification_uri":"https://auth.kimi.com/verify","expires_in":900}`, true},
		{"malformed", `secret-access`, false},
		{"missing", `{}`, false},
		{"empty device", strings.Replace(kimiDeviceJSON, "secret-device", "", 1), false},
		{"empty user", strings.Replace(kimiDeviceJSON, "USER-CODE", "", 1), false},
		{"unsafe base", strings.Replace(kimiDeviceJSON, `"https://auth.kimi.com/verify"`, `"javascript:secret-access"`, 1), false},
		{"unsafe complete", strings.Replace(kimiDeviceJSON, `https://auth.kimi.com/verify?code=USER-CODE`, `javascript:secret-access`, 1), false},
		{"unknown base", strings.Replace(kimiDeviceJSON, `"https://auth.kimi.com/verify"`, `"https://example.com/verify"`, 1), false},
		{"unknown complete", strings.Replace(kimiDeviceJSON, `https://auth.kimi.com/verify?code=USER-CODE`, `https://example.com/verify?code=USER-CODE`, 1), false},
		{"expired", strings.Replace(kimiDeviceJSON, `"expires_in":900`, `"expires_in":0`, 1), false},
		{"overflow", strings.Replace(kimiDeviceJSON, `"expires_in":900`, `"expires_in":9223372036854775807`, 1), false},
		{"negative interval", strings.Replace(kimiDeviceJSON, `"interval":2`, `"interval":-1`, 1), false},
		{"string interval", strings.Replace(kimiDeviceJSON, `"interval":2`, `"interval":"secret-access"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow := kimiTestFlow(t, func(*http.Request) (*http.Response, error) { return kimiResponse(200, tc.body), nil })
			device, err := flow.Start(context.Background())
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "defaults" && (device.interval != 5*time.Second || device.VerificationURL != "https://auth.kimi.ai/verify") {
					t.Fatalf("device = %+v", device)
				}
			} else {
				kimiAssertSafeError(t, err)
			}
		})
	}
}

func kimiAssertSafeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"secret-access", "secret-refresh", "secret-device"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %s: %v", secret, err)
		}
	}
}

func TestKimiPollFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", 400, `{"error":"access_denied","error_description":"secret-device"}`},
		{"expired", 400, `{"error":"expired_token"}`},
		{"unknown", 400, `{"error":"secret-access","error_description":"secret-refresh"}`},
		{"server", 503, `{"error":"authorization_pending"}`},
		{"non-json", 400, `secret-refresh`},
		{"missing token", 200, `{"access_token":"secret-access"}`},
		{"malformed token", 200, `secret-access`},
		{"invalid lifetime", 200, strings.Replace(kimiTokenJSON, "3600", "-1", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polls := 0
			flow := kimiTestFlow(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == kimiDeviceEP {
					return kimiResponse(200, kimiDeviceJSON), nil
				}
				polls++
				return kimiResponse(tc.status, tc.body), nil
			})
			device, err := flow.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			kimiAssertSafeError(t, flow.Poll(context.Background(), device))
			if polls != 1 {
				t.Errorf("polls = %d", polls)
			}
			if _, err := flow.Store.Load("kimi"); err == nil {
				t.Error("saved failed login")
			}
		})
	}
}

func TestKimiPollExpiryAndCancel(t *testing.T) {
	for _, action := range []string{"already expired", "expires while waiting", "expires after pending", "canceled", "cancel while waiting", "nil device"} {
		t.Run(action, func(t *testing.T) {
			polls := 0
			flow := kimiTestFlow(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == kimiDeviceEP {
					return kimiResponse(200, kimiDeviceJSON), nil
				}
				polls++
				return kimiResponse(400, `{"error":"authorization_pending"}`), nil
			})
			device, err := flow.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expectedPolls := 0
			switch action {
			case "already expired":
				device.ExpiresAt = flow.now().Add(-time.Second)
			case "expires while waiting":
				device.ExpiresAt = flow.now().Add(time.Second)
			case "expires after pending":
				device.ExpiresAt = flow.now().Add(3 * time.Second)
				expectedPolls = 1
			case "canceled":
				cancel()
			case "cancel while waiting":
				flow.wait = func(context.Context, time.Duration) error { cancel(); return nil }
			case "nil device":
				device = nil
			}
			err = flow.Poll(ctx, device)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(action, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if polls != expectedPolls {
				t.Errorf("polls = %d, want %d", polls, expectedPolls)
			}
		})
	}
}

func TestKimiTokenSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		valid  bool
	}{
		{"rotates", 200, kimiTokenJSON, true},
		{"revoked", 400, `{"error":"invalid_grant","error_description":"secret-refresh"}`, false},
		{"missing refresh", 200, `{"access_token":"secret-access","expires_in":3600}`, false},
		{"invalid lifetime", 200, strings.Replace(kimiTokenJSON, "3600", "0", 1), false},
		{"overflow lifetime", 200, strings.Replace(kimiTokenJSON, "3600", "9223372036854775807", 1), false},
		{"invalid type", 200, strings.Replace(kimiTokenJSON, "3600", `"secret-access"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
			old := Token{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour)}
			if err := store.Save("kimi", old); err != nil {
				t.Fatal(err)
			}
			requests := 0
			source := NewKimiTokenSource(store, &http.Client{Transport: kimiTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				kimiCheckRequest(t, r, kimiTokenEP, url.Values{"client_id": {kimiClientID}, "grant_type": {"refresh_token"}, "refresh_token": {"old-refresh"}})
				return kimiResponse(tc.status, tc.body), nil
			})})
			before := time.Now()
			access, err := source.AccessToken(context.Background())
			if tc.valid {
				if err != nil || access != "secret-access" {
					t.Fatalf("AccessToken = %q, %v", access, err)
				}
				if _, err := source.AccessToken(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				kimiAssertSafeError(t, err)
			}
			if requests != 1 {
				t.Errorf("requests = %d", requests)
			}
			token, err := store.Load("kimi")
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid {
				if token.AccessToken != access || token.RefreshToken != "secret-refresh" || token.ExpiresAt.Before(before.Add(time.Hour)) {
					t.Fatalf("saved token = %+v", token)
				}
			} else if token.AccessToken != old.AccessToken {
				t.Fatal("failed refresh overwrote credentials")
			}
		})
	}
}

func TestKimiRequestsBounded(t *testing.T) {
	for _, operation := range []string{"start", "poll", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := &http.Client{Transport: kimiTransport(func(r *http.Request) (*http.Response, error) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}
				flow := NewKimiDeviceFlow(&Store{Path: filepath.Join(t.TempDir(), "credentials.json")}, client)
				start := time.Now()
				var err error
				wantDuration := kimiRequestTimeout
				switch operation {
				case "start":
					_, err = flow.Start(context.Background())
				case "poll":
					wantDuration = 10 * time.Second
					err = flow.Poll(context.Background(), &KimiDeviceAuth{deviceCode: "secret-device", interval: time.Second, ExpiresAt: start.Add(wantDuration)})
				case "refresh":
					_, err = NewKimiTokenSource(flow.Store, client).refresh(context.Background(), "secret-refresh")
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error = %v", err)
				}
				if elapsed := time.Since(start); elapsed != wantDuration {
					t.Errorf("elapsed = %v, want %v", elapsed, wantDuration)
				}
			})
		})
	}
}

func TestKimiRequestSanitizesFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"http", 400, `secret-access secret-refresh secret-device`, nil},
		{"decode", 200, `{"device_code":"secret-device","expires_in":"secret-refresh"}`, nil},
		{"transport", 0, "", errors.New("secret-access secret-refresh")},
		{"oversize", 200, strings.Repeat("x", kimiMaxResponseBytes+1), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow := kimiTestFlow(t, func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return kimiResponse(tc.status, tc.body), nil
			})
			_, err := flow.Start(context.Background())
			kimiAssertSafeError(t, err)
		})
	}
}

func TestKimiRejectsRedirects(t *testing.T) {
	forwarded := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			forwarded = true
			json.NewEncoder(w).Encode(map[string]string{"access_token": "secret-access"})
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, status, err := kimiRequest(context.Background(), srv.Client(), srv.URL, url.Values{"refresh_token": {"secret-refresh"}})
	if err != nil || status != 307 || forwarded {
		t.Fatalf("redirect: status=%d forwarded=%v error=%v", status, forwarded, err)
	}
}

func TestKimiRequestCancellation(t *testing.T) {
	for _, operation := range []string{"start", "poll", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			flow := kimiTestFlow(t, func(r *http.Request) (*http.Response, error) {
				cancel()
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			var err error
			switch operation {
			case "start":
				_, err = flow.Start(ctx)
			case "poll":
				err = flow.Poll(ctx, &KimiDeviceAuth{deviceCode: "secret-device", interval: time.Second, ExpiresAt: flow.now().Add(time.Minute)})
			case "refresh":
				_, err = NewKimiTokenSource(flow.Store, flow.HTTPC).refresh(ctx, "secret-refresh")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type kimiBrokenBody struct{ closed bool }

func (*kimiBrokenBody) Read([]byte) (int, error) {
	return 0, errors.New("secret-access secret-refresh")
}
func (b *kimiBrokenBody) Close() error { b.closed = true; return nil }

func TestKimiReadFailureClosesBody(t *testing.T) {
	body := &kimiBrokenBody{}
	flow := kimiTestFlow(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
	})
	_, err := flow.Start(context.Background())
	kimiAssertSafeError(t, err)
	if !body.closed {
		t.Error("response body not closed")
	}
}

func TestKimiPersistenceFailures(t *testing.T) {
	for _, operation := range []string{"poll", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			flow := kimiTestFlow(t, nil)
			flow.HTTPC.Transport = kimiTransport(func(*http.Request) (*http.Response, error) {
				// Make persistence fail after a successful token exchange.
				flow.Store.Path = t.TempDir()
				return kimiResponse(200, kimiTokenJSON), nil
			})
			var err error
			if operation == "poll" {
				err = flow.Poll(context.Background(), &KimiDeviceAuth{deviceCode: "secret-device", interval: time.Second, ExpiresAt: flow.now().Add(time.Minute)})
			} else {
				if err := flow.Store.Save("kimi", Token{RefreshToken: "secret-refresh"}); err != nil {
					t.Fatal(err)
				}
				_, err = NewKimiTokenSource(flow.Store, flow.HTTPC).AccessToken(context.Background())
			}
			kimiAssertSafeError(t, err)
		})
	}
}

func TestKimiTryPollIntervalAndCompletion(t *testing.T) {
	requests := 0
	flow := kimiTestFlow(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == kimiDeviceEP {
			return kimiResponse(200, kimiDeviceJSON), nil
		}
		requests++
		switch requests {
		case 1:
			return kimiResponse(400, `{"error":"authorization_pending"}`), nil
		case 2:
			return kimiResponse(400, `{"error":"slow_down"}`), nil
		case 3:
			return kimiResponse(400, `{"error":"slow_down","interval":1}`), nil
		default:
			return kimiResponse(200, kimiTokenJSON), nil
		}
	})
	ctx := context.Background()
	device, err := flow.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !device.ExpiresAt.Equal(flow.now().Add(900 * time.Second)) {
		t.Fatalf("ExpiresAt = %v", device.ExpiresAt)
	}
	for _, step := range []struct {
		advance  time.Duration
		requests int
		done     bool
	}{
		{0, 0, false}, // Start enforces the provider's initial interval.
		{time.Second, 0, false},
		{time.Second, 1, false},
		{0, 1, false},
		{time.Second, 1, false},
		{time.Second, 2, false}, // slow_down increases 2s to 7s.
		{6 * time.Second, 2, false},
		{time.Second, 3, false}, // Server interval=1 must not decrease the RFC minimum.
		{11 * time.Second, 3, false},
		{time.Second, 4, true},
		{0, 4, true},
		{time.Hour, 4, true}, // Completion stays done even after original device expiry.
	} {
		if err := flow.wait(ctx, step.advance); err != nil {
			t.Fatal(err)
		}
		done, err := flow.TryPoll(ctx, device)
		if err != nil || done != step.done || requests != step.requests {
			t.Fatalf("advance %v: done=%v requests=%d error=%v; want done=%v requests=%d", step.advance, done, requests, err, step.done, step.requests)
		}
	}
	if _, err := flow.Store.Load("kimi"); err != nil {
		t.Fatal(err)
	}
}

func TestKimiTryPollConcurrent(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	flow := kimiTestFlow(t, func(*http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(started)
			<-release
		}
		return kimiResponse(200, kimiTokenJSON), nil
	})
	device := &KimiDeviceAuth{deviceCode: "secret-device", interval: time.Second, ExpiresAt: flow.now().Add(time.Minute)}
	type result struct {
		done bool
		err  error
	}
	finished := make(chan result, 1)
	go func() { done, err := flow.TryPoll(context.Background(), device); finished <- result{done, err} }()
	<-started
	for range 5 {
		done, err := flow.TryPoll(context.Background(), device)
		if done || err != nil {
			t.Errorf("concurrent poll = %v, %v", done, err)
		}
	}
	close(release)
	result1 := <-finished
	if !result1.done || result1.err != nil {
		t.Fatalf("first poll = %v", result1)
	}
	for range 5 {
		done, err := flow.TryPoll(context.Background(), device)
		if !done || err != nil {
			t.Errorf("completed poll = %v, %v", done, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestKimiTryPollExpiryCancelAndFailure(t *testing.T) {
	for _, mode := range []string{"expired", "canceled", "denied", "persistence"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			flow := kimiTestFlow(t, func(*http.Request) (*http.Response, error) {
				requests++
				if mode == "denied" {
					return kimiResponse(400, `{"error":"access_denied"}`), nil
				}
				return kimiResponse(200, kimiTokenJSON), nil
			})
			device := &KimiDeviceAuth{deviceCode: "secret-device", interval: time.Second, ExpiresAt: flow.now().Add(time.Minute)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantRequests := 0
			switch mode {
			case "expired":
				device.ExpiresAt = flow.now()
			case "canceled":
				cancel()
			case "denied":
				wantRequests = 1
			case "persistence":
				flow.Store.Path = t.TempDir()
				wantRequests = 1
			}
			for range 2 {
				done, err := flow.TryPoll(ctx, device)
				if done {
					t.Fatal("failed poll reported success")
				}
				kimiAssertSafeError(t, err)
				if mode == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v", err)
				}
				flow.wait(context.Background(), time.Second)
			}
			if requests != wantRequests {
				t.Fatalf("requests = %d, want %d", requests, wantRequests)
			}
		})
	}
}

func TestKimiPollSharesTryPollSchedule(t *testing.T) {
	requests := 0
	flow := kimiTestFlow(t, func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return kimiResponse(400, `{"error":"slow_down","interval":20}`), nil
		}
		return kimiResponse(200, kimiTokenJSON), nil
	})
	device := &KimiDeviceAuth{deviceCode: "secret-device", interval: 2 * time.Second, ExpiresAt: flow.now().Add(time.Minute)}
	if done, err := flow.TryPoll(context.Background(), device); done || err != nil {
		t.Fatalf("first poll = %v, %v", done, err)
	}
	var waits []time.Duration
	wait := flow.wait
	flow.wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return wait(ctx, d) }
	if err := flow.Poll(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(waits, []time.Duration{20 * time.Second}) || requests != 2 {
		t.Fatalf("waits=%v requests=%d", waits, requests)
	}
}
