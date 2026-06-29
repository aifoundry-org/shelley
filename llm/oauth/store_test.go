package oauth

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	store := &Store{Path: path}

	if _, err := store.Load("anthropic"); err == nil {
		t.Fatal("expected error loading from missing file")
	}

	tok := Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Unix(1000, 0).UTC()}
	if err := store.Save("anthropic", tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load("anthropic")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != tok {
		t.Errorf("Load = %+v, want %+v", got, tok)
	}
}

func TestStorePreservesOtherProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := &Store{Path: path}
	if err := store.Save("anthropic", Token{AccessToken: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("openai", Token{AccessToken: "o"}); err != nil {
		t.Fatal(err)
	}
	a, err := store.Load("anthropic")
	if err != nil || a.AccessToken != "a" {
		t.Fatalf("anthropic clobbered: %+v %v", a, err)
	}
	o, err := store.Load("openai")
	if err != nil || o.AccessToken != "o" {
		t.Fatalf("openai missing: %+v %v", o, err)
	}
}

func TestStoreFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := &Store{Path: path}
	if err := store.Save("anthropic", Token{AccessToken: "a"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != fs.FileMode(0o600) {
		t.Errorf("file perm = %o, want 600", perm)
	}
}

func TestStoreDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := &Store{Path: path}
	store.Save("anthropic", Token{AccessToken: "a"})
	store.Save("openai", Token{AccessToken: "o"})
	if err := store.Delete("anthropic"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Load("anthropic"); err == nil {
		t.Error("anthropic still present after Delete")
	}
	if o, err := store.Load("openai"); err != nil || o.AccessToken != "o" {
		t.Errorf("openai disturbed by delete: %+v %v", o, err)
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	fresh := Token{ExpiresAt: now.Add(time.Hour)}
	if fresh.Expired(now, time.Minute) {
		t.Error("fresh token reported expired")
	}
	near := Token{ExpiresAt: now.Add(30 * time.Second)}
	if !near.Expired(now, time.Minute) {
		t.Error("token within skew not reported expired")
	}
}
