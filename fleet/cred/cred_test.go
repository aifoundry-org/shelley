package cred

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/fleet"
	"shelley.exe.dev/llm/oauth"
)

type fakeFleet struct {
	node       *fleet.Node
	nodeErr    error
	meta       map[string]string
	metaErr    error
	setMetaErr error
}

func TestRefreshFinishingAfterTakeoverDoesNotSeizeOwnership(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	syncFleet := func() {
		na.Sync(ctx)
		nb.Sync(ctx)
	}
	if err := a.Save("anthropic", tok("original")); err != nil {
		t.Fatal(err)
	}
	syncFleet()
	started, finish := make(chan struct{}), make(chan struct{})
	a.HTTPC = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "console.anthropic.com" {
			t.Error("unexpected refresh host")
		}
		close(started)
		<-finish
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
			`{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`,
		))}, nil
	})}
	result := make(chan error, 1)
	go func() { result <- a.TokenSource("anthropic").Refresh(ctx) }()
	<-started
	if err := b.Save("anthropic", tok("takeover")); err != nil {
		t.Fatal(err)
	}
	syncFleet()
	close(finish)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	syncFleet()
	if b.Describe("anthropic") != "owner (epoch 2)" || !strings.HasPrefix(a.Describe("anthropic"), "following b (epoch 2") {
		t.Fatal("old owner's refresh seized fleet ownership")
	}
	if local, err := a.Local.Load("anthropic"); err != nil || local.RefreshToken != "rotated-refresh" {
		t.Fatal("yielding owner lost its local rotated token family", err)
	}
	if follower, err := a.Load("anthropic"); err != nil || follower.AccessToken != "at-takeover" || follower.RefreshToken != "" {
		t.Fatal("old owner did not follow new owner", err)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReconcileImportedLoginDoesNotSeizeOwnership(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	if err := a.Save("anthropic", tok("owner")); err != nil {
		t.Fatal(err)
	}
	na.Sync(ctx)
	nb.Sync(ctx)
	// An import writes local storage directly, never the ownership-claim API.
	if err := b.Local.Save("anthropic", tok("imported")); err != nil {
		t.Fatal(err)
	}
	if err := b.ReconcileLocal(ctx); err != nil {
		t.Fatal(err)
	}
	na.Sync(ctx)
	nb.Sync(ctx)
	if a.Describe("anthropic") != "owner (epoch 1)" || !strings.HasPrefix(b.Describe("anthropic"), "following a (epoch 1") {
		t.Fatal("import seized ownership from the live owner")
	}
	if token, err := b.Load("anthropic"); err != nil || token.RefreshToken != "" {
		t.Fatal("follower received a refresh token", err)
	}
	if local, err := b.Local.Load("anthropic"); err != nil || local.RefreshToken != "imported" {
		t.Fatal("follower lost local imported credential", err)
	}
}

func (f *fakeFleet) Node() (*fleet.Node, error)    { return f.node, f.nodeErr }
func (f *fakeFleet) Meta(k string) (string, error) { return f.meta[k], f.metaErr }
func (f *fakeFleet) SetMeta(k, v string) error {
	if f.setMetaErr != nil {
		return f.setMetaErr
	}
	f.meta[k] = v
	return nil
}

func newManager(t *testing.T, n *fleet.Node) *Manager {
	return &Manager{
		Fleet:  &fakeFleet{node: n, meta: map[string]string{}},
		Local:  localStore(t),
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
	m := &Manager{Local: localStore(t), Logger: slog.Default()}
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
	if err := b.Local.Save("anthropic", tok("rt-b")); err != nil {
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

func TestStatus(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	creds := func(m *Manager) map[string]*CredentialStatus {
		return m.Status()["credentials"].(map[string]*CredentialStatus)
	}

	// Nobody owns anything: every provider is present and null.
	for _, m := range []*Manager{a, b} {
		got := creds(m)
		if len(got) != len(Providers) {
			t.Fatalf("providers = %v", got)
		}
		for p, c := range got {
			if c != nil {
				t.Errorf("%s owned before login: %+v", p, c)
			}
		}
	}

	if err := a.Save("anthropic", tok("rt-a1")); err != nil {
		t.Fatal(err)
	}
	na.Sync(ctx)
	nb.Sync(ctx)
	b.reconcile(ctx, "anthropic", false)

	owner, follower := creds(a)["anthropic"], creds(b)["anthropic"]
	if owner == nil || !owner.Self || owner.Owner != "a" || owner.OwnerID != na.ID() || owner.Epoch != 1 {
		t.Errorf("owner view: %+v", owner)
	}
	if follower == nil || follower.Self || follower.Owner != "a" || follower.OwnerID != na.ID() || follower.Epoch != 1 {
		t.Errorf("follower view: %+v", follower)
	}
	if owner.ExpiresAt.IsZero() || !owner.ExpiresAt.Equal(follower.ExpiresAt) {
		t.Errorf("expires_at: owner %v follower %v", owner.ExpiresAt, follower.ExpiresAt)
	}
	if creds(b)["openai"] != nil {
		t.Error("openai owned")
	}

	// Outside a fleet there is nothing to report.
	if got := (&Manager{Local: a.Local, Logger: a.Logger}).Status()["credentials"].(map[string]*CredentialStatus); got["anthropic"] != nil {
		t.Errorf("no-fleet status: %+v", got)
	}
}

func localStore(t *testing.T) *oauth.DBStore {
	t.Helper()
	database, err := db.New(db.Config{DSN: t.TempDir() + "/shelley.db"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &oauth.DBStore{DB: database}
}

type faultNode struct {
	*fleet.Node
	listErr, putErr, deleteErr error
	puts, deletes              int
}

func (n *faultNode) List(ctx context.Context, prefix string) ([]fleet.Entry, error) {
	if n.listErr != nil {
		return nil, n.listErr
	}
	return n.Node.List(ctx, prefix)
}

func (n *faultNode) Put(ctx context.Context, key string, value any) error {
	n.puts++
	if n.putErr != nil {
		return n.putErr
	}
	return n.Node.Put(ctx, key, value)
}

func (n *faultNode) Delete(ctx context.Context, key string) error {
	n.deletes++
	if n.deleteErr != nil {
		return n.deleteErr
	}
	return n.Node.Delete(ctx, key)
}

func injectNode(m *Manager) *faultNode {
	f := m.Fleet.(*fakeFleet)
	n := &faultNode{Node: f.node}
	m.getNode = func() (credentialNode, error) { return n, f.nodeErr }
	return n
}

func TestFleetReadErrorsFailClosed(t *testing.T) {
	for _, failure := range []string{"node", "list"} {
		for _, localPresent := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/local=%t", failure, localPresent), func(t *testing.T) {
				ctx := context.Background()
				na := fleet.LoopbackNode(t, "a")
				nb := fleet.LoopbackNode(t, "b", na.Addr())
				a, b := newManager(t, na), newManager(t, nb)
				sync := func() { na.Sync(ctx); nb.Sync(ctx) }
				for _, m := range []*Manager{a, b, a} {
					if err := m.Save("anthropic", tok("local-"+m.Fleet.(*fakeFleet).node.ID())); err != nil {
						t.Fatal(err)
					}
					sync()
				}
				if !localPresent {
					if err := b.Local.Delete("anthropic"); err != nil {
						t.Fatal(err)
					}
				}
				// b has a lower-epoch entry and, normally, a stale local family.
				before, err := nb.List(ctx, "cred/")
				if err != nil {
					t.Fatal(err)
				}
				f := b.Fleet.(*fakeFleet)
				epoch := f.meta[metaKey("anthropic", "maxepoch")]
				n := injectNode(b)
				unavailable := errors.New("injected fleet storage failure")
				if failure == "node" {
					f.nodeErr = unavailable
				} else {
					n.listErr = unavailable
				}
				refreshes := 0
				b.HTTPC = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
					refreshes++
					return nil, errors.New("unexpected refresh")
				})}
				if _, err := b.Load("anthropic"); !errors.Is(err, unavailable) {
					t.Fatal("Load did not propagate fleet failure")
				}
				if _, err := b.TokenSource("anthropic").AccessToken(ctx); !errors.Is(err, unavailable) {
					t.Fatal("token source did not propagate fleet failure")
				}
				if err := b.reconcile(ctx, "anthropic", false); !errors.Is(err, unavailable) {
					t.Fatal("reconcile did not propagate fleet failure")
				}
				b.refreshIfDue(ctx, "anthropic")
				if err := b.ReconcileLocal(ctx); !errors.Is(err, unavailable) {
					t.Fatal("import reconciliation did not report partial failure")
				}
				after, err := nb.List(ctx, "cred/")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || epoch != f.meta[metaKey("anthropic", "maxepoch")] {
					t.Fatal("fleet failure changed credentials or epoch")
				}
				if n.puts != 0 || n.deletes != 0 || refreshes != 0 {
					t.Fatal("fleet failure attempted publication, retraction, or refresh")
				}
				f.nodeErr, n.listErr = nil, nil
				if err := b.reconcile(ctx, "anthropic", false); err != nil {
					t.Fatal(err)
				}
				got, err := b.Load("anthropic")
				if err != nil || got.AccessToken != "at-local-a" || got.RefreshToken != "" {
					t.Fatal("recovered follower did not follow the current owner", err)
				}
				if localPresent {
					local, err := b.Local.Load("anthropic")
					if err != nil || local.RefreshToken != "local-b" {
						t.Fatal("follower lost its local family", err)
					}
				}
			})
		}
	}
}

func TestNotJoinedIsStandalone(t *testing.T) {
	m := &Manager{
		Fleet: &fakeFleet{nodeErr: fmt.Errorf("membership: %w", fleet.ErrNotJoined)},
		Local: localStore(t), Logger: slog.Default(),
	}
	original, refreshed := tok("original"), tok("refreshed")
	if err := m.Save("openai", original); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveRefreshed("openai", original, refreshed); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Load("openai"); err != nil || got.RefreshToken != refreshed.RefreshToken {
		t.Fatal("not-joined manager did not use local credentials", err)
	}
	if err := m.ReconcileLocal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.pendingClaims) != 0 {
		t.Fatal("standalone login retained a fleet ownership claim")
	}
	if err := m.Delete("openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load("openai"); !errors.Is(err, oauth.ErrNoCredentials) {
		t.Fatal("standalone logout did not delete local credentials")
	}
}

func TestDisconnectedFleetUsesLocalReplica(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	if err := a.Save("anthropic", tok("owner")); err != nil {
		t.Fatal(err)
	}
	na.Sync(ctx)
	nb.Sync(ctx)
	if err := b.Local.Save("anthropic", tok("stale")); err != nil {
		t.Fatal(err)
	}
	// Stop transport/sync, leaving the fleet SQLite stores healthy.
	na.Close()
	nb.Close()
	if err := b.ReconcileLocal(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Load("anthropic"); err != nil || got.AccessToken != "at-owner" || got.RefreshToken != "" {
		t.Fatal("disconnected follower ignored its local fleet replica", err)
	}
	original, err := a.Local.Load("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveRefreshed("anthropic", original, tok("rotated")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Load("anthropic"); err != nil || got.RefreshToken != "rotated" {
		t.Fatal("disconnected owner could not persist and publish its rotation", err)
	}
}

func TestFleetMutationErrorsRetainLocalPersistenceAndRetry(t *testing.T) {
	for _, operation := range []string{"login", "refresh", "logout"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			na := fleet.LoopbackNode(t, "a")
			nb := fleet.LoopbackNode(t, "b", na.Addr())
			a, b := newManager(t, na), newManager(t, nb)
			sync := func() { na.Sync(ctx); nb.Sync(ctx) }
			original := tok("original")
			if err := a.Save("anthropic", original); err != nil {
				t.Fatal(err)
			}
			sync()
			m := a
			if operation == "login" {
				m = b // A failed takeover must retain its login claim on retry.
			}
			n := injectNode(m)
			failure := errors.New("injected fleet write failure")
			n.putErr = failure
			var err error
			switch operation {
			case "login":
				err = m.Save("anthropic", tok("new"))
			case "refresh":
				err = m.SaveRefreshed("anthropic", original, tok("new"))
			case "logout":
				n.putErr, n.deleteErr = nil, failure
				err = m.Delete("anthropic")
			}
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), "locally") {
				t.Fatal("mutation did not explicitly report partial persistence")
			}
			local, loadErr := m.Local.Load("anthropic")
			if operation == "logout" {
				if !errors.Is(loadErr, oauth.ErrNoCredentials) {
					t.Fatal("failed retraction restored local credentials")
				}
			} else if loadErr != nil || local.RefreshToken != "new" {
				t.Fatal("failed publication undid local persistence", loadErr)
			}
			sync()
			if got, err := b.Load("anthropic"); err != nil || got.AccessToken != original.AccessToken {
				t.Fatal("failed write changed the fleet-visible credential", err)
			}
			n.putErr, n.deleteErr = nil, nil
			if err := m.reconcile(ctx, "anthropic", false); err != nil {
				t.Fatal(err)
			}
			sync()
			if operation == "logout" {
				if _, err := b.Load("anthropic"); !errors.Is(err, oauth.ErrNoCredentials) {
					t.Fatal("retraction retry did not remove fleet credentials")
				}
			} else {
				got, err := a.Load("anthropic")
				if err != nil || got.AccessToken != "at-new" {
					t.Fatal("retry did not publish locally persisted credentials", err)
				}
				expectedEpoch := 1
				if operation == "login" {
					expectedEpoch = 2
				}
				_, best, _, err := m.best(ctx, "anthropic")
				if err != nil || best == nil || best.Epoch != expectedEpoch || best.owner != n.ID() {
					t.Fatal("retry changed login/refresh ownership semantics", err)
				}
				if operation == "login" && !strings.Contains(a.Describe("anthropic"), "following b") {
					t.Fatal("takeover retry did not make the previous owner a follower")
				}
			}
			if len(m.pendingClaims) != 0 {
				t.Fatal("successful retry retained pending ownership intent")
			}
		})
	}
}

func TestImportedPublicationFailureReportsPartialPersistence(t *testing.T) {
	ctx := context.Background()
	n := fleet.LoopbackNode(t, "a")
	m := newManager(t, n)
	if err := m.Local.Save("anthropic", tok("imported")); err != nil {
		t.Fatal(err)
	}
	fault := injectNode(m)
	failure := errors.New("injected publication failure")
	fault.putErr = failure
	changes := 0
	m.OnChange = func() {
		changes++
		for _, mu := range []*sync.Mutex{&m.mu, &m.reconcileMu} {
			if !mu.TryLock() {
				t.Error("change callback ran with a manager lock held")
			} else {
				mu.Unlock()
			}
		}
	}
	err := m.ReconcileLocal(ctx)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "local credentials retained") {
		t.Fatal("import did not explicitly report retained local persistence")
	}
	if changes != 1 {
		t.Fatal("failed import publication did not check for local credential changes")
	}
	if local, err := m.Local.Load("anthropic"); err != nil || local.RefreshToken != "imported" {
		t.Fatal("failed publication lost imported local credentials", err)
	}
	if _, ok, err := n.Get(ctx, "cred/anthropic/"+n.ID()); err != nil || ok {
		t.Fatal("failed import publication changed fleet state", err)
	}
	fault.putErr = nil
	if err := m.ReconcileLocal(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Describe("anthropic") != "owner (epoch 1)" {
		t.Fatal("import publication did not succeed on retry")
	}
}

func TestFailedLoginRefreshDoesNotClaimButReconciliationRetries(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	if err := a.Save("anthropic", tok("owner")); err != nil {
		t.Fatal(err)
	}
	na.Sync(ctx)
	nb.Sync(ctx)
	n := injectNode(b)
	n.putErr = errors.New("injected login publication failure")
	login := tok("login")
	if err := b.Save("anthropic", login); err == nil {
		t.Fatal("failed takeover reported success")
	}
	n.putErr = nil
	if err := b.SaveRefreshed("anthropic", login, tok("refreshed")); err != nil {
		t.Fatal(err)
	}
	if n.puts != 1 || !strings.HasPrefix(b.Describe("anthropic"), "following a") {
		t.Fatal("nonclaim refresh attempted to claim ownership")
	}
	if err := b.reconcile(ctx, "anthropic", false); err != nil {
		t.Fatal(err)
	}
	if b.Describe("anthropic") != "owner (epoch 2)" {
		t.Fatal("reconciliation did not retry the original login claim")
	}
	if local, err := b.Local.Load("anthropic"); err != nil || local.RefreshToken != "refreshed" {
		t.Fatal("claim retry did not retain the latest rotated family", err)
	}
}

type loadFailureStore struct {
	oauth.Credentials
	err error
}

func (s loadFailureStore) Load(string) (oauth.Token, error) { return oauth.Token{}, s.err }

func TestLocalReadFailureDoesNotRetract(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, fleet.LoopbackNode(t, "a"))
	if err := m.Save("anthropic", tok("owner")); err != nil {
		t.Fatal(err)
	}
	n := injectNode(m)
	failure := errors.New("injected local storage failure")
	m.Local = loadFailureStore{Credentials: m.Local, err: failure}
	if err := m.reconcile(ctx, "anthropic", false); !errors.Is(err, failure) {
		t.Fatal("local storage failure was treated as missing credentials")
	}
	if n.puts != 0 || n.deletes != 0 {
		t.Fatal("local read failure changed publication")
	}
}

func TestEpochMetadataErrorsAreNotIgnored(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			m := newManager(t, fleet.LoopbackNode(t, "a"))
			f := m.Fleet.(*fakeFleet)
			failure := errors.New("injected metadata failure")
			if operation == "read" {
				f.metaErr = failure
			} else {
				f.setMetaErr = failure
			}
			if err := m.Save("anthropic", tok("login")); !errors.Is(err, failure) {
				t.Fatal("metadata failure did not propagate")
			}
			if local, err := m.Local.Load("anthropic"); err != nil || local.RefreshToken != "login" {
				t.Fatal("metadata failure undid local persistence", err)
			}
			f.metaErr, f.setMetaErr = nil, nil
			if err := m.reconcile(context.Background(), "anthropic", false); err != nil {
				t.Fatal(err)
			}
			if m.Describe("anthropic") != "owner (epoch 1)" {
				t.Fatal("metadata retry changed ownership epoch")
			}
		})
	}
}

func TestMalformedFleetRecordFailsClosed(t *testing.T) {
	ctx := context.Background()
	node := fleet.LoopbackNode(t, "a")
	m := newManager(t, node)
	if err := m.Save("anthropic", tok("stale")); err != nil {
		t.Fatal(err)
	}
	canary := "sensitive-credential-canary"
	key := "cred/anthropic/other-owner"
	if err := node.Put(ctx, key, map[string]any{
		"epoch": 99, "access_token": []string{canary},
	}); err != nil {
		t.Fatal(err)
	}
	n := injectNode(m)
	f := m.Fleet.(*fakeFleet)
	epoch := f.meta[metaKey("anthropic", "maxepoch")]
	refreshes := 0
	m.HTTPC = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		refreshes++
		return nil, errors.New("unexpected refresh")
	})}
	_, loadErr := m.Load("anthropic")
	for _, err := range []error{loadErr, m.reconcile(ctx, "anthropic", false), m.TokenSource("anthropic").Refresh(ctx)} {
		if err == nil {
			t.Fatal("malformed fleet credential did not fail closed")
		}
		if strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "anthropic") {
			t.Fatal("malformed credential error is not safely redacted and attributed")
		}
	}
	m.refreshIfDue(ctx, "anthropic")
	if n.puts != 0 || n.deletes != 0 || refreshes != 0 || epoch != f.meta[metaKey("anthropic", "maxepoch")] {
		t.Fatal("malformed fleet record caused publication, retraction, refresh, or an epoch change")
	}
	if err := node.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Load("anthropic"); err != nil || got.RefreshToken != "stale" {
		t.Fatal("manager did not recover after corrupt fleet record was retracted", err)
	}
}

func TestSameAccessTokenPublishesChangedExpiryAndAccount(t *testing.T) {
	for _, change := range []string{"none", "expiry", "account"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			na := fleet.LoopbackNode(t, "a")
			nb := fleet.LoopbackNode(t, "b", na.Addr())
			a, b := newManager(t, na), newManager(t, nb)
			original := tok("original")
			original.ExpiresAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := a.Save("anthropic", original); err != nil {
				t.Fatal(err)
			}
			before, ok, err := na.Get(ctx, "cred/anthropic/"+na.ID())
			if err != nil || !ok {
				t.Fatal("missing original fleet credential", err)
			}
			refreshed := original
			refreshed.RefreshToken = "rotated"
			switch change {
			case "expiry":
				refreshed.ExpiresAt = original.ExpiresAt.Add(time.Hour)
			case "account":
				refreshed.AccountID = "new-account"
			}
			n := injectNode(a)
			if err := a.SaveRefreshed("anthropic", original, refreshed); err != nil {
				t.Fatal(err)
			}
			after, ok, err := na.Get(ctx, before.Key)
			if err != nil || !ok {
				t.Fatal("missing refreshed fleet credential", err)
			}
			if change == "none" {
				if n.puts != 0 || !reflect.DeepEqual(before, after) {
					t.Fatal("unchanged published fields rewrote publication time/state")
				}
			} else if n.puts != 1 || before.HLC == after.HLC {
				t.Fatal("changed published fields were not republished")
			}
			na.Sync(ctx)
			nb.Sync(ctx)
			got, err := b.Load("anthropic")
			if err != nil || !got.ExpiresAt.Equal(refreshed.ExpiresAt) || got.AccountID != refreshed.AccountID || got.AccessToken != original.AccessToken || got.RefreshToken != "" {
				t.Fatal("follower received stale expiry/account or a refresh token", err)
			}
			if a.Describe("anthropic") != "owner (epoch 1)" {
				t.Fatal("same-access-token refresh changed ownership epoch")
			}
		})
	}
}

func TestPublishedClaimWithMetadataFailureDoesNotStealFromLaterLogin(t *testing.T) {
	ctx := context.Background()
	na := fleet.LoopbackNode(t, "a")
	nb := fleet.LoopbackNode(t, "b", na.Addr())
	a, b := newManager(t, na), newManager(t, nb)
	if err := a.Save("anthropic", tok("a")); err != nil {
		t.Fatal(err)
	}
	na.Sync(ctx)
	nb.Sync(ctx)
	// Remember epoch 1 so B's metadata failure occurs after its epoch-2 Put,
	// not while recording the previously observed owner before publication.
	if err := b.Local.Save("anthropic", tok("stale-b")); err != nil {
		t.Fatal(err)
	}
	if err := b.reconcile(ctx, "anthropic", false); err != nil {
		t.Fatal(err)
	}
	f := b.Fleet.(*fakeFleet)
	failure := errors.New("injected post-publication metadata failure")
	f.setMetaErr = failure
	n := injectNode(b)
	if err := b.Save("anthropic", tok("b")); !errors.Is(err, failure) {
		t.Fatal("post-publication metadata failure did not propagate")
	}
	_, published, _, err := b.best(ctx, "anthropic")
	if err != nil || published == nil || published.owner != nb.ID() || published.Epoch != 2 || n.puts != 1 {
		t.Fatal("B's takeover was not published before metadata failed", err)
	}
	nc := fleet.LoopbackNode(t, "c", nb.Addr())
	c := newManager(t, nc)
	if err := c.Save("anthropic", tok("c")); err != nil {
		t.Fatal(err)
	}
	nc.Sync(ctx)
	nb.Sync(ctx)
	f.setMetaErr = nil
	if err := b.reconcile(ctx, "anthropic", false); err != nil {
		t.Fatal(err)
	}
	_, best, mine, err := b.best(ctx, "anthropic")
	if err != nil || best == nil || best.owner != nc.ID() || best.Epoch != 3 || mine != nil || n.puts != 1 {
		t.Fatal("metadata retry reclaimed ownership from C's later login", err)
	}
	if f.meta[metaKey("anthropic", "maxepoch")] != "3" || len(b.pendingClaims) != 0 {
		t.Fatal("nonclaim retry did not reconstruct the epoch and clear claim intent")
	}
	if local, err := b.Local.Load("anthropic"); err != nil || local.RefreshToken != "b" {
		t.Fatal("metadata failure or retry lost B's local token family", err)
	}
	if got, err := b.Load("anthropic"); err != nil || got.AccessToken != "at-c" || got.RefreshToken != "" {
		t.Fatal("B did not remain a follower of C", err)
	}
}
