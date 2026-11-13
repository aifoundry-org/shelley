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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

type credentialNode interface {
	ID() string
	Ready() <-chan struct{}
	Get(context.Context, string) (fleet.Entry, bool, error)
	List(context.Context, string) ([]fleet.Entry, error)
	Put(context.Context, string, any) error
	Delete(context.Context, string) error
}

// Manager implements oauth.Credentials: the on-disk store when this node owns
// the credential (or is not in a fleet), the fleet-published token otherwise.
// Fleet may be nil, in which case Manager is a plain pass-through to Local.
type Manager struct {
	Fleet  Fleet
	Local  oauth.Credentials
	HTTPC  *http.Client
	Logger *slog.Logger
	// OnChange is called when the set of providers with usable credentials
	// may have changed (so the model catalog can be rebuilt).
	OnChange func()

	mu      sync.Mutex
	sources map[string]*oauth.TokenSource
	sig     string
	// Serialize local mutations and publication, so logout cannot be followed
	// by a stale reconciliation publishing its pre-logout snapshot.
	reconcileMu sync.Mutex
	// A login whose publication failed still claims ownership on retry.
	// Protected by reconcileMu; refreshes never create claims. This intent is
	// in memory only: after restarting, a failed login must be retried to claim
	// ownership from another node.
	pendingClaims map[string]bool
	// Optional node lookup for fault-injection tests.
	getNode func() (credentialNode, error)
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
	n, best, _, err := m.best(context.Background(), provider)
	if err != nil {
		return oauth.Token{}, err
	}
	if best != nil && best.owner != n.ID() {
		return best.token(), nil
	}
	return m.Local.Load(provider)
}

// Save persists a new login and claims fleet ownership. TokenSource refreshes
// must use SaveRefreshed instead.
func (m *Manager) Save(provider string, tok oauth.Token) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if err := m.Local.Save(provider, tok); err != nil {
		return err
	}
	if err := m.reconcileLocked(context.Background(), provider, true); err != nil {
		return fmt.Errorf("saved %s credentials locally, but fleet publication failed (retry login after a restart): %w", provider, err)
	}
	return nil
}

// SaveRefreshed persists only if the original login is still current. Refresh
// never seizes ownership from another fleet node.
func (m *Manager) SaveRefreshed(provider string, previous, refreshed oauth.Token) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if err := m.Local.SaveRefreshed(provider, previous, refreshed); err != nil {
		return err
	}
	if err := m.reconcileLocked(context.Background(), provider, false); err != nil {
		return fmt.Errorf("saved refreshed %s credentials locally, but fleet reconciliation failed: %w", provider, err)
	}
	return nil
}

// Delete logs out locally and retracts this node's published entry.
func (m *Manager) Delete(provider string) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if err := m.Local.Delete(provider); err != nil {
		return err
	}
	delete(m.pendingClaims, provider)
	if err := m.reconcileLocked(context.Background(), provider, false); err != nil {
		return fmt.Errorf("deleted %s credentials locally, but fleet retraction failed: %w", provider, err)
	}
	return nil
}

// ReconcileLocal discovers imported local logins without treating them as new
// fleet ownership claims. Existing owners retain their epoch and token family.
// Errors report partial success: locally imported credentials are never undone.
func (m *Manager) ReconcileLocal(ctx context.Context) error {
	defer m.checkChanged()
	if m.Fleet == nil {
		return nil
	}
	n, err := m.node()
	if errors.Is(err, fleet.ErrNotJoined) {
		return nil
	}
	if err == nil {
		select {
		case <-n.Ready():
			for _, p := range Providers {
				err = errors.Join(err, m.reconcile(ctx, p, false))
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("local credentials retained, but fleet reconciliation failed: %w", err)
	}
	return nil
}

// Owner returns the name of the other fleet node whose login this node is
// using for provider, or "" if this node owns it or no one does.
func (m *Manager) Owner(provider string) string {
	n, best, _, _ := m.best(context.Background(), provider)
	if n == nil || best == nil || best.owner == n.ID() {
		return ""
	}
	return m.nodeName(n, best.owner)
}

func (m *Manager) nodeName(n credentialNode, id string) string {
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
		n, best, _, _ := m.best(context.Background(), p)
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
	n, best, _, _ := m.best(context.Background(), provider)
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
		n, err := m.node()
		if err == nil {
			select {
			case <-n.Ready(): // don't claim before hearing the fleet's state
				for _, p := range Providers {
					if err := m.reconcile(ctx, p, false); err != nil {
						m.Logger.Error("fleet cred reconcile", "provider", p, "error", err)
						continue
					}
					m.refreshIfDue(ctx, p)
				}
			case <-ctx.Done():
				return
			}
		} else if !errors.Is(err, fleet.ErrNotJoined) {
			m.Logger.Error("fleet cred node", "error", err)
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
	n, best, _, err := m.best(ctx, provider)
	if err != nil {
		m.Logger.Error("fleet cred refresh ownership", "provider", provider, "error", err)
		return
	}
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
func (m *Manager) reconcile(ctx context.Context, provider string, claim bool) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	return m.reconcileLocked(ctx, provider, claim || m.pendingClaims[provider])
}

func (m *Manager) reconcileLocked(ctx context.Context, provider string, claim bool) error {
	if claim {
		if m.pendingClaims == nil {
			m.pendingClaims = make(map[string]bool)
		}
		m.pendingClaims[provider] = true
	}
	n, best, mine, err := m.best(ctx, provider)
	if err != nil {
		return err
	}
	if n == nil {
		delete(m.pendingClaims, provider)
		return nil
	}
	me := n.ID()
	key := "cred/" + provider + "/" + me
	local, err := m.Local.Load(provider)
	if err != nil && !errors.Is(err, oauth.ErrNoCredentials) {
		return fmt.Errorf("load local %s credentials: %w", provider, err)
	}
	if errors.Is(err, oauth.ErrNoCredentials) || local.RefreshToken == "" {
		if mine != nil {
			if err := n.Delete(ctx, key); err != nil {
				return fmt.Errorf("retract %s credentials: %w", provider, err)
			}
			m.Logger.Info("fleet cred retracted", "provider", provider)
		}
		delete(m.pendingClaims, provider)
		return nil
	}
	maxSeen, err := m.metaInt(provider, "maxepoch")
	if err != nil {
		return err
	}
	if best != nil && best.Epoch > maxSeen {
		maxSeen = best.Epoch
		if err := m.setMeta(provider, "maxepoch", fmt.Sprint(maxSeen)); err != nil {
			return err
		}
	}
	if best != nil && best.owner != me && !claim {
		if mine != nil {
			if err := n.Delete(ctx, key); err != nil {
				return fmt.Errorf("yield %s credentials: %w", provider, err)
			}
			m.Logger.Info("fleet cred yielded, now following", "provider", provider, "owner", best.owner, "epoch", best.Epoch)
		}
		return nil
	}
	epoch := maxSeen + 1
	owned := mine != nil && (best == nil || best.owner == me)
	if owned {
		if mine.AccessToken == local.AccessToken && mine.ExpiresAt.Equal(local.ExpiresAt) && mine.AccountID == local.AccountID {
			delete(m.pendingClaims, provider)
			return nil
		}
		epoch = mine.Epoch
	}
	e := entry{Epoch: epoch, AccessToken: local.AccessToken, ExpiresAt: local.ExpiresAt, AccountID: local.AccountID, Published: time.Now().UTC()}
	if err := n.Put(ctx, key, e); err != nil {
		return fmt.Errorf("publish %s credentials: %w", provider, err)
	}
	// Publication completes the login claim even if remembering its epoch
	// fails. Retrying that claim could otherwise steal from a later login.
	delete(m.pendingClaims, provider)
	if !owned {
		if err := m.setMeta(provider, "maxepoch", fmt.Sprint(epoch)); err != nil {
			return err
		}
		m.Logger.Info("fleet cred claimed", "provider", provider, "epoch", epoch)
	}
	return nil
}

func (m *Manager) node() (credentialNode, error) {
	if m.getNode != nil {
		return m.getNode()
	}
	return m.Fleet.Node()
}

// best returns the running node (nil if not in a fleet), the highest-epoch
// entry for provider, and this node's own entry. Only ErrNotJoined is absence;
// other node/list errors must not allow using a local refresh family.
func (m *Manager) best(ctx context.Context, provider string) (n credentialNode, best, mine *entry, err error) {
	if m.Fleet == nil {
		return nil, nil, nil, nil
	}
	n, err = m.node()
	if errors.Is(err, fleet.ErrNotJoined) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fleet credential node: %w", err)
	}
	prefix := "cred/" + provider + "/"
	entries, err := n.List(ctx, prefix)
	if err != nil {
		return n, nil, nil, fmt.Errorf("list fleet %s credentials: %w", provider, err)
	}
	for _, kv := range entries {
		var e entry
		if json.Unmarshal(kv.Value, &e) != nil {
			// Decoder errors may contain token material. A corrupt record
			// might be the current owner, so neither ignore it nor log it.
			return n, nil, nil, fmt.Errorf("invalid fleet %s credential record", provider)
		}
		e.owner = strings.TrimPrefix(kv.Key, prefix)
		if e.owner == n.ID() {
			mine = &e
		}
		if best == nil || e.Epoch > best.Epoch || (e.Epoch == best.Epoch && e.owner > best.owner) {
			best = &e
		}
	}
	return n, best, mine, nil
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

func (m *Manager) setMeta(provider, k, v string) error {
	if err := m.Fleet.SetMeta(metaKey(provider, k), v); err != nil {
		return fmt.Errorf("write fleet credential epoch: %w", err)
	}
	return nil
}

func (m *Manager) metaInt(provider, k string) (int, error) {
	s, err := m.Fleet.Meta(metaKey(provider, k))
	if err != nil {
		return 0, fmt.Errorf("read fleet credential epoch: %w", err)
	}
	if s == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, errors.New("invalid fleet credential epoch")
	}
	return v, nil
}

func metaKey(provider, k string) string { return "cred/" + provider + "/" + k }
