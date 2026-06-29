// Package oauth implements the OAuth 2.0 + PKCE flows Shelley uses to
// authenticate to AI providers with subscription credentials instead of
// inference API keys. It is provider-agnostic at the primitive level
// (PKCE, token store) with provider-specific endpoint shaping layered on top.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE holds a Proof Key for Code Exchange pair (RFC 7636).
type PKCE struct {
	Verifier  string // random secret, sent on token exchange
	Challenge string // BASE64URL(SHA256(Verifier)), sent on authorize
	Method    string // always "S256"
}

// NewPKCE generates a fresh PKCE pair using the S256 method.
func NewPKCE() (PKCE, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		Method:    "S256",
	}, nil
}
