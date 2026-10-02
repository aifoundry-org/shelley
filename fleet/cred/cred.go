// Package cred shares subscription credentials across a fleet.
//
// The node that logs in to a provider is that credential's owner: it alone
// holds the refresh token (on disk, as without a fleet) and publishes the
// short-lived access token to the fleet under cred/<provider>/<node-id>.
// Everyone else follows: they use the published access token and never
// refresh. Among the entries for a provider, the highest epoch wins; a login
// anywhere claims max+1, so an operator moves ownership simply by logging in
// on another node. The former owner notices the higher epoch and yields; its
// own token stays on disk and is used again if the current owner goes away.
package cred

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/fleet"
	"shelley.exe.dev/llm/oauth"
)

// Providers are the subscription providers shelley knows how to log in to.
var Providers = []string{"anthropic", "openai", "kimi"}

const tick = time.Minute

// entry is the replicated value at cred/<provider>/<node-id>.
type entry struct {
	Epoch       int       `json:"epoch"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	AccountID   string    `json:"account_id,omitempty"`
	Published   time.Time `json:"published"`
	owner       string    // node id, from the key
}

func (e *entry) token() oauth.Token {
	return oauth.Token{AccessToken: e.AccessToken, ExpiresAt: e.ExpiresAt, AccountID: e.AccountID}
}

// Fleet is what Manager needs from fleet.Service.
type Fleet interface {
	Node() (*fleet.Node, error)
	Meta(key string) (string, error)
	SetMeta(key, value string) error
}

// Manager implements oauth.Credentials: the on-disk store when this node owns
// the credential (or is not in a fleet), the fleet-published token otherwise.
// Fleet may be nil, in which case Manager is a plain pass-through to Disk.
type Manager struct {
	Fleet  Fleet
	Disk   *oauth.Store
	HTTPC  *http.Client
	Logger *slog.Logger
	// OnChange is called when the set of providers with usable credentials
	// may have changed (so the model catalog can be rebuilt).
	OnChange func()

	mu      sync.Mutex
	sources map[string]*oauth.TokenSource
	sig     string
}

var _ oauth.Credentials = (*Manager)(nil)

// TokenSource returns the single TokenSource for provider. Sharing one per
// provider matters: refreshes rotate the refresh token, so they must be
// serialized.
func (m *Manager) TokenSource(provider string) *oauth.TokenSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sources == nil {
		m.sources = map[string]*oauth.TokenSource{}
	}
	if ts, ok := m.sources[provider]; ok {
		return ts
	}
	var ts *oauth.TokenSource
	switch provider {
	case "anthropic":
		ts = oauth.NewAnthropicTokenSource(m, m.HTTPC)
	case "openai":
		ts = oauth.NewOpenAITokenSource(m, m.HTTPC)
	case "kimi":
		ts = oauth.NewKimiTokenSource(m, m.HTTPC)
	default:
		panic("cred: unknown provider " + provider)
	}
	m.sources[provider] = ts
	return ts
}

// Load returns the credential to use: the fleet-published token if another
// node owns it, else whatever is on disk.
func (m *Manager) Load(provider string) (oauth.Token, error) {
	if n, best, _ := m.best(context.Background(), provider); best != nil && best.owner != n.ID() {
		return best.token(), nil
	}
	return m.Disk.Load(provider)
}

// Save persists a token (from login or refresh) and publishes it if this
// node owns, or now claims, the credential.
func (m *Manager) Save(provider string, tok oauth.Token) error {
	if err := m.Disk.Save(provider, tok); err != nil {
		return err
	}
	m.reconcile(context.Background(), provider, true)
	return nil
}

// Delete logs out locally and retracts this node's published entry.
func (m *Manager) Delete(provider string) error {
	if err := m.Disk.Delete(provider); err != nil {
		return err
	}
	m.reconcile(context.Background(), provider, false)
	return nil
}

// Owner returns the name of the other fleet node whose login this node is
// using for provider, or "" if this node owns it or no one does.
func (m *Manager) Owner(provider string) string {
	n, best, _ := m.best(context.Background(), provider)
	if n == nil || best == nil || best.owner == n.ID() {
		return ""
	}
	return m.nodeName(n, best.owner)
}

func (m *Manager) nodeName(n *fleet.Node, id string) string {
	if e, ok, _ := n.Get(context.Background(), "node/"+id); ok {
		var info fleet.NodeInfo
		if json.Unmarshal(e.Value, &info) == nil {
			return info.Name
		}
	}
	return id
}

// CredentialStatus is the fleet-wide view of one provider's credential.
type CredentialStatus struct {
	Owner     string    `json:"owner"`    // owner node name
	OwnerID   string    `json:"owner_id"` // owner node id
	Epoch     int       `json:"epoch"`
	ExpiresAt time.Time `json:"expires_at"`
	Self      bool      `json:"self"` // this node is the owner
}

// Status returns the fields this manager contributes to GET /api/fleet:
// "credentials", a map from provider to its owner, or null if unowned.
func (m *Manager) Status() map[string]any {
	creds := map[string]*CredentialStatus{}
	for _, p := range Providers {
		n, best, _ := m.best(context.Background(), p)
		if n == nil || best == nil {
			creds[p] = nil
			continue
		}
		creds[p] = &CredentialStatus{
			Owner: m.nodeName(n, best.owner), OwnerID: best.owner,
			Epoch: best.Epoch, ExpiresAt: best.ExpiresAt, Self: best.owner == n.ID(),
		}
	}
	return map[string]any{"credentials": creds}
}

// Describe reports this node's role for provider, or "" outside a fleet.
func (m *Manager) Describe(provider string) string {
	n, best, _ := m.best(context.Background(), provider)
	if n == nil || best == nil {
		return ""
	}
	if best.owner == n.ID() {
		return fmt.Sprintf("owner (epoch %d)", best.Epoch)
	}
	name, seen := best.owner, "never"
	if e, ok, _ := n.Get(context.Background(), "node/"+best.owner); ok {
		var info fleet.NodeInfo
		if json.Unmarshal(e.Value, &info) == nil {
			name = info.Name
			seen = time.Since(info.Seen).Truncate(time.Second).String() + " ago"
		}
	}
	return fmt.Sprintf("following %s (epoch %d, owner last seen %s)", name, best.Epoch, seen)
}

// Run reconciles periodically until ctx is done: claims new logins, steps
// down when superseded, refreshes owned tokens at half TTL, and fires
// OnChange when the usable provider set changes.
func (m *Manager) Run(ctx context.Context) {
	if m.Fleet == nil {
		return
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		n, err := m.Fleet.Node()
		if err == nil {
			select {
			case <-n.Ready(): // don't claim before hearing the fleet's state
				for _, p := range Providers {
					m.reconcile(ctx, p, false)
					m.refreshIfDue(ctx, p)
				}
			case <-ctx.Done():
				return
			}
		}
		m.checkChanged()
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) refreshIfDue(ctx context.Context, provider string) {
	n, best, _ := m.best(ctx, provider)
	if best == nil || best.owner != n.ID() {
		return
	}
	half := best.Published.Add(best.ExpiresAt.Sub(best.Published) / 2)
	if time.Now().Before(half) {
		return
	}
	if err := m.TokenSource(provider).Refresh(ctx); err != nil {
		m.Logger.Error("fleet cred refresh", "provider", provider, "error", err)
	}
}

// reconcile brings this node's published entry in line with its disk state
// and the fleet's view of ownership. claim is true when a login just
// happened here, which is the one act that takes ownership from a live
// owner. Rules, in order:
//
//   - no usable local token: retract anything we published.
//   - another node owns and this is not a login: yield (retract, follow).
//     The local token stays on disk and is used again if that owner goes
//     away or logs out.
//   - otherwise publish: a claim takes maxSeenEpoch+1; a refresh keeps the
//     epoch and just updates the access token.
func (m *Manager) reconcile(ctx context.Context, provider string, claim bool) {
	n, best, mine := m.best(ctx, provider)
	if n == nil {
		return
	}
	me := n.ID()
	key := "cred/" + provider + "/" + me
	local, err := m.Disk.Load(provider)
	if err != nil || local.RefreshToken == "" {
		if mine != nil {
			n.Delete(ctx, key)
			m.Logger.Info("fleet cred retracted", "provider", provider)
		}
		return
	}
	maxSeen := m.metaInt(provider, "maxepoch")
	if best != nil && best.Epoch > maxSeen {
		maxSeen = best.Epoch
		m.setMeta(provider, "maxepoch", fmt.Sprint(maxSeen))
	}
	if best != nil && best.owner != me && !claim {
		if mine != nil {
			n.Delete(ctx, key)
			m.Logger.Info("fleet cred yielded, now following", "provider", provider, "owner", best.owner, "epoch", best.Epoch)
		}
		return
	}
	epoch := maxSeen + 1
	owned := mine != nil && (best == nil || best.owner == me)
	if owned {
		if mine.AccessToken == local.AccessToken {
			return
		}
		epoch = mine.Epoch
	}
	e := entry{Epoch: epoch, AccessToken: local.AccessToken, ExpiresAt: local.ExpiresAt, AccountID: local.AccountID, Published: time.Now().UTC()}
	if err := n.Put(ctx, key, e); err != nil {
		m.Logger.Error("fleet cred publish", "provider", provider, "error", err)
		return
	}
	if !owned {
		m.setMeta(provider, "maxepoch", fmt.Sprint(epoch))
		m.Logger.Info("fleet cred claimed", "provider", provider, "epoch", epoch)
	}
}

// best returns the running node (nil if not in a fleet), the highest-epoch
// entry for provider, and this node's own entry.
func (m *Manager) best(ctx context.Context, provider string) (n *fleet.Node, best, mine *entry) {
	if m.Fleet == nil {
		return nil, nil, nil
	}
	n, err := m.Fleet.Node()
	if err != nil {
		return nil, nil, nil
	}
	prefix := "cred/" + provider + "/"
	entries, err := n.List(ctx, prefix)
	if err != nil {
		m.Logger.Error("fleet cred list", "provider", provider, "error", err)
		return n, nil, nil
	}
	for _, kv := range entries {
		var e entry
		if json.Unmarshal(kv.Value, &e) != nil {
			continue
		}
		e.owner = strings.TrimPrefix(kv.Key, prefix)
		if e.owner == n.ID() {
			mine = &e
		}
		if best == nil || e.Epoch > best.Epoch || (e.Epoch == best.Epoch && e.owner > best.owner) {
			best = &e
		}
	}
	return n, best, mine
}

// checkChanged fires OnChange when the set of providers Load succeeds for
// differs from last time.
func (m *Manager) checkChanged() {
	var sig []string
	for _, p := range Providers {
		if _, err := m.Load(p); err == nil {
			sig = append(sig, p)
		}
	}
	s := strings.Join(sig, ",")
	m.mu.Lock()
	changed := m.sig != s
	m.sig = s
	m.mu.Unlock()
	if changed && m.OnChange != nil {
		m.OnChange()
	}
}

func (m *Manager) setMeta(provider, k, v string) {
	if err := m.Fleet.SetMeta(metaKey(provider, k), v); err != nil {
		m.Logger.Error("fleet cred meta", "error", err)
	}
}

func (m *Manager) metaInt(provider, k string) int {
	var v int
	if s, _ := m.Fleet.Meta(metaKey(provider, k)); s != "" {
		fmt.Sscan(s, &v)
	}
	return v
}

func metaKey(provider, k string) string { return "cred/" + provider + "/" + k }
