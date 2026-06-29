package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"testing"
)

func TestNewPKCEUnique(t *testing.T) {
	a, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	b, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if a.Verifier == b.Verifier {
		t.Fatal("two PKCE verifiers were identical; not random")
	}
}

// RFC 7636: code_verifier is 43-128 chars from the unreserved set
// [A-Za-z0-9-._~]. We emit base64url (no padding), a subset of that.
func TestVerifierFormat(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if n := len(p.Verifier); n < 43 || n > 128 {
		t.Errorf("verifier length = %d, want 43..128", n)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._~-]+$`).MatchString(p.Verifier) {
		t.Errorf("verifier has invalid chars: %q", p.Verifier)
	}
}

// RFC 7636: challenge = BASE64URL(SHA256(verifier)) for method S256.
func TestChallengeIsS256OfVerifier(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if p.Challenge != want {
		t.Errorf("Challenge = %q, want %q", p.Challenge, want)
	}
	if p.Method != "S256" {
		t.Errorf("Method = %q, want S256", p.Method)
	}
}
