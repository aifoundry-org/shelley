package fleet

import (
	"context"
	"log/slog"
	"testing"
)

// LoopbackNode starts a node on in-process TCP for tests in other packages.
// It joins the given addresses and is closed when the test ends.
func LoopbackNode(t testing.TB, name string, join ...string) *Node {
	t.Helper()
	store, err := OpenStore(t.TempDir() + "/fleet.db")
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

// Sync runs one synchronous sync round with every known peer.
func (n *Node) Sync(ctx context.Context) { n.syncRound(ctx, len(n.Peers())) }
