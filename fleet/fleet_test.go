package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
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
	n, err := start(context.Background(), name, store, tr, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close(); store.Close() })
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

func TestLeaveRetractsRosterEntry(t *testing.T) {
	ctx := context.Background()
	a := testNode(t, "a")
	b := testNode(t, "b", a.Addr())
	syncAll(ctx, a, b)
	if len(a.Peers()) != 1 {
		t.Fatalf("a peers = %+v", a.Peers())
	}
	// What Service.Leave does before stopping the node.
	if _, err := b.store.Append(ctx, b.ID(), "node/"+b.ID(), nil); err != nil {
		t.Fatal(err)
	}
	b.syncRound(ctx, 1)
	if got := a.Peers(); len(got) != 0 {
		t.Errorf("a still has peers after b left: %+v", got)
	}
}

func TestStaleNodeDropped(t *testing.T) {
	ctx := context.Background()
	a := testNode(t, "a")
	b := testNode(t, "b", a.Addr())
	syncAll(ctx, a, b)

	// A node "x" that heartbeated 25h ago and is not reachable.
	old, _ := json.Marshal(NodeInfo{Name: "x", Addr: "127.0.0.1:1", Seen: time.Now().Add(-25 * time.Hour)})
	if _, err := a.store.Append(ctx, "x", "node/x", old); err != nil {
		t.Fatal(err)
	}
	a.loadRoster(ctx)
	for _, p := range a.Peers() {
		if p.ID == "x" {
			t.Fatal("stale node in peer set")
		}
	}

	// Heartbeat tombstones it; b learns the tombstone.
	if err := a.heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	syncAll(ctx, a, b)
	for _, n := range []*Node{a, b} {
		if _, ok, _ := n.Get(ctx, "node/x"); ok {
			t.Errorf("%s: node/x still live", n.ID())
		}
	}

	// x comes back: its newer heartbeat wins over the tombstone.
	fresh, _ := json.Marshal(NodeInfo{Name: "x", Addr: "127.0.0.1:1", Seen: time.Now()})
	if _, err := b.store.Append(ctx, "x", "node/x", fresh); err != nil {
		t.Fatal(err)
	}
	syncAll(ctx, a, b)
	if _, ok, _ := a.Get(ctx, "node/x"); !ok {
		t.Error("a: returning node not restored")
	}
}

func TestHeartbeatWritesOnlyWhenNeeded(t *testing.T) {
	ctx := context.Background()
	a := testNode(t, "a") // start already heartbeated once
	seq := func() int64 { vv, _ := a.store.Version(ctx); return vv[a.ID()] }
	before := seq()
	for range 3 {
		if err := a.heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := seq(); got != before {
		t.Errorf("heartbeat wrote %d ops with nothing changed", got-before)
	}
	// Seen older than seenRefresh → rewrite.
	old, _ := json.Marshal(NodeInfo{Name: "a", Addr: a.Addr(), Seen: time.Now().Add(-seenRefresh - time.Minute)})
	if _, err := a.store.Append(ctx, a.ID(), "node/"+a.ID(), old); err != nil {
		t.Fatal(err)
	}
	before = seq()
	if err := a.heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if got := seq(); got != before+1 {
		t.Errorf("expected one refresh op, got %d", got-before)
	}
}
