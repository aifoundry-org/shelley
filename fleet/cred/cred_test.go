package cred

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/fleet"
	"shelley.exe.dev/llm/oauth"
)

type fakeFleet struct {
	node *fleet.Node
	meta map[string]string
}

func (f *fakeFleet) Node() (*fleet.Node, error)    { return f.node, nil }
func (f *fakeFleet) Meta(k string) (string, error) { return f.meta[k], nil }
func (f *fakeFleet) SetMeta(k, v string) error     { f.meta[k] = v; return nil }

func newManager(t *testing.T, n *fleet.Node) *Manager {
	return &Manager{
		Fleet:  &fakeFleet{node: n, meta: map[string]string{}},
		Disk:   &oauth.Store{Path: t.TempDir() + "/credentials.json"},
		Logger: slog.Default(),
	}
}

func tok(rt string) oauth.Token {
	return oauth.Token{AccessToken: "at-" + rt, RefreshToken: rt, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestOwnershipLifecycle(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	sync := func() { na.Sync(ctx); nb.Sync(ctx) }

	// Nobody logged in: Load fails everywhere.
	if _, err := b.Load("anthropic"); err == nil {
		t.Fatal("b has credentials before anyone logged in")
	}

	// a logs in → owner, epoch 1. b follows and sees the access token only.
	if err := a.Save("anthropic", tok("rt-a1")); err != nil {
		t.Fatal(err)
	}
	sync()
	got, err := b.Load("anthropic")
	if err != nil || got.AccessToken != "at-rt-a1" || got.RefreshToken != "" {
		t.Fatalf("b follower token = %+v, %v", got, err)
	}
	if d := a.Describe("anthropic"); d != "owner (epoch 1)" {
		t.Errorf("a: %q", d)
	}
	if d := b.Describe("anthropic"); !strings.HasPrefix(d, "following a (epoch 1") {
		t.Errorf("b: %q", d)
	}

	// a refreshes (refresh token rotates): same epoch, new access token.
	if err := a.Save("anthropic", tok("rt-a2")); err != nil {
		t.Fatal(err)
	}
	sync()
	if got, _ := b.Load("anthropic"); got.AccessToken != "at-rt-a2" {
		t.Errorf("rotation not published: %+v", got)
	}
	if d := a.Describe("anthropic"); d != "owner (epoch 1)" {
		t.Errorf("rotation bumped epoch: %q", d)
	}

	// Operator logs in on b → b claims epoch 2; a steps down and follows.
	if err := b.Save("anthropic", tok("rt-b1")); err != nil {
		t.Fatal(err)
	}
	sync()
	a.reconcile(ctx, "anthropic", false) // what Run does each tick
	sync()
	if d := b.Describe("anthropic"); d != "owner (epoch 2)" {
		t.Errorf("b: %q", d)
	}
	if d := a.Describe("anthropic"); !strings.HasPrefix(d, "following b (epoch 2") {
		t.Errorf("a: %q", d)
	}
	if got, _ := a.Load("anthropic"); got.AccessToken != "at-rt-b1" || got.RefreshToken != "" {
		t.Errorf("a should use b's token: %+v", got)
	}
	// a's entry is gone and reconcile stays put while b is live.
	if _, ok, _ := na.Get(ctx, "cred/anthropic/a"); ok {
		t.Error("a's entry not retracted")
	}
	a.reconcile(ctx, "anthropic", false)
	if d := a.Describe("anthropic"); !strings.HasPrefix(d, "following b") {
		t.Errorf("a re-claimed while b owns: %q", d)
	}

	// b logs out → a's still-valid token re-claims automatically (epoch 3).
	if err := b.Delete("anthropic"); err != nil {
		t.Fatal(err)
	}
	sync()
	a.reconcile(ctx, "anthropic", false)
	sync()
	if d := a.Describe("anthropic"); d != "owner (epoch 3)" {
		t.Errorf("a after owner logout: %q", d)
	}
	if got, _ := b.Load("anthropic"); got.AccessToken != "at-rt-a2" {
		t.Errorf("b should follow a again: %+v", got)
	}

	// a logs out → entry retracted; nobody has credentials.
	if err := a.Delete("anthropic"); err != nil {
		t.Fatal(err)
	}
	sync()
	if _, err := b.Load("anthropic"); err == nil {
		t.Error("b still has credentials after owner logged out")
	}
}

func TestNoFleetPassthrough(t *testing.T) {
	m := &Manager{Disk: &oauth.Store{Path: t.TempDir() + "/c.json"}, Logger: slog.Default()}
	if err := m.Save("openai", tok("x")); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Load("openai"); err != nil || got.RefreshToken != "x" {
		t.Fatalf("%+v %v", got, err)
	}
	if m.Describe("openai") != "" {
		t.Error("describe outside fleet")
	}
}

func TestJoinWithLocalCredsDoesNotSeize(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	a := newManager(t, na)
	if err := a.Save("anthropic", tok("rt-a")); err != nil {
		t.Fatal(err)
	}
	// b already had its own login before joining the fleet.
	nb := fleet.LoopbackNode(t, "b")
	b := newManager(t, nb)
	if err := b.Disk.Save("anthropic", tok("rt-b")); err != nil {
		t.Fatal(err)
	}
	if err := nb.Join(ctx, na.Addr()); err != nil {
		t.Fatal(err)
	}
	b.reconcile(ctx, "anthropic", false) // periodic tick after join
	na.Sync(ctx)
	if d := a.Describe("anthropic"); d != "owner (epoch 1)" {
		t.Errorf("joining node seized ownership: a=%q b=%q", d, b.Describe("anthropic"))
	}
	if got, _ := b.Load("anthropic"); got.AccessToken != "at-rt-a" {
		t.Errorf("b should follow a: %+v", got)
	}
	// a logs out → b's local token takes over automatically.
	if err := a.Delete("anthropic"); err != nil {
		t.Fatal(err)
	}
	nb.Sync(ctx)
	b.reconcile(ctx, "anthropic", false)
	na.Sync(ctx)
	if d := b.Describe("anthropic"); d != "owner (epoch 2)" {
		t.Errorf("b after a logout: %q", d)
	}
	if got, _ := a.Load("anthropic"); got.AccessToken != "at-rt-b" {
		t.Errorf("a should follow b: %+v", got)
	}
}
