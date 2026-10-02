package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
)

func testNode(t *testing.T, name string, join ...string) *Node {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := newLoopbackTransport(name)
	if err != nil {
		t.Fatal(err)
	}
	n, err := start(context.Background(), Config{Name: name}, store, tr, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	for _, addr := range join {
		if err := n.Join(context.Background(), addr); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func get(t *testing.T, n *Node, key string) (string, bool) {
	t.Helper()
	e, ok, err := n.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if ok {
		json.Unmarshal(e.Value, &s)
	}
	return s, ok
}

func syncAll(ctx context.Context, nodes ...*Node) {
	for range 2 { // two rounds: enough for transitive discovery in a line topology
		for _, n := range nodes {
			n.syncRound(ctx, len(n.Peers()))
		}
	}
}

func TestReplicationAndDiscovery(t *testing.T) {
	ctx := context.Background()
	a := testNode(t, "a")
	b := testNode(t, "b", a.Addr())
	c := testNode(t, "c", b.Addr()) // c only knows b; must learn a via roster

	if err := a.Put(ctx, "k", "from-a"); err != nil {
		t.Fatal(err)
	}
	syncAll(ctx, a, b, c)
	for _, n := range []*Node{a, b, c} {
		if v, _ := get(t, n, "k"); v != "from-a" {
			t.Errorf("%s: k=%q", n.ID(), v)
		}
	}

	// Everyone knows everyone.
	for _, n := range []*Node{a, b, c} {
		if got := len(n.Peers()); got != 2 {
			t.Errorf("%s: %d peers, want 2: %+v", n.ID(), got, n.Peers())
		}
	}

	// Later write wins; delete propagates.
	if err := c.Put(ctx, "k", "from-c"); err != nil {
		t.Fatal(err)
	}
	syncAll(ctx, a, b, c)
	if v, _ := get(t, a, "k"); v != "from-c" {
		t.Errorf("a: k=%q, want from-c", v)
	}
	if err := b.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	syncAll(ctx, a, b, c)
	if _, ok := get(t, c, "k"); ok {
		t.Error("c: k still present after delete")
	}
}

func TestJoinFailure(t *testing.T) {
	a := testNode(t, "a")
	if err := a.Join(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("join of dead address succeeded")
	}
	if len(a.Peers()) != 0 {
		t.Errorf("failed join left peers: %+v", a.Peers())
	}
}

func TestOpsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ops := []Op{{Node: "x", Seq: 1, HLC: 5, Key: "k", Value: json.RawMessage(`1`)}, {Node: "y", Seq: 1, HLC: 5, Key: "k", Value: json.RawMessage(`2`)}}
	for range 2 {
		if err := s.Apply(ctx, ops); err != nil {
			t.Fatal(err)
		}
	}
	e, _, _ := s.Get(ctx, "k")
	if string(e.Value) != "2" || e.Node != "y" { // HLC tie: higher node id wins
		t.Errorf("got %s from %s", e.Value, e.Node)
	}
	vv, _ := s.Version(ctx)
	if vv["x"] != 1 || vv["y"] != 1 {
		t.Errorf("vv = %v", vv)
	}
	after, _ := s.OpsAfter(ctx, VersionVector{"x": 1})
	if len(after) != 1 || after[0].Node != "y" {
		t.Errorf("OpsAfter = %+v", after)
	}
}
