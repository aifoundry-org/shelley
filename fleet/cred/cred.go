// Package cred shares subscription credentials across a fleet.
//
// The node that logs in to a provider is that credential's owner: it alone
// holds the refresh token (on disk, as without a fleet) and publishes the
// short-lived access token to the fleet under cred/<provider>/<node-id>.
// Everyone else follows: they use the published access token and never
// refresh. Among the entries for a provider, the highest epoch wins; a login
// anywhere claims max+1, so an operator moves ownership simply by logging in
// on another node. The former owner notices the higher epoch and steps down.
package cred

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	tok, err := m.Disk.Load(provider)
	if err != nil {
		return tok, err
	}
	if m.Fleet != nil {
		if sup, _ := m.Fleet.Meta(metaKey(provider, "superseded")); sup != "" && sup == hash(tok.RefreshToken) {
			return oauth.Token{}, fmt.Errorf("%s login on this node was superseded by another fleet member and the owner has since logged out; log in again", provider)
		}
	}
	return tok, nil
}

// Save persists a token (from login or refresh) and publishes it if this
// node owns, or now claims, the credential.
func (m *Manager) Save(provider string, tok oauth.Token) error {
	prev, _ := m.Disk.Load(provider)
	if err := m.Disk.Save(provider, tok); err != nil {
		return err
	}
	if m.Fleet != nil {
		// A refresh rotates the refresh token; keep tracking the family we own
		// so reconcile sees a rotation, not a new login.
		if rt, _ := m.Fleet.Meta(metaKey(provider, "rt")); rt != "" && rt == hash(prev.RefreshToken) {
			m.Fleet.SetMeta(metaKey(provider, "rt"), hash(tok.RefreshToken))
		}
	}
	m.reconcile(context.Background(), provider)
	return nil
}

// Delete logs out locally and retracts this node's published entry.
func (m *Manager) Delete(provider string) error {
	if err := m.Disk.Delete(provider); err != nil {
		return err
	}
	m.reconcile(context.Background(), provider)
	return nil
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
					m.reconcile(ctx, p)
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
// and the fleet's view of ownership.
func (m *Manager) reconcile(ctx context.Context, provider string) {
	n, best, mine := m.best(ctx, provider)
	if n == nil {
		return
	}
	me := n.ID()
	key := "cred/" + provider + "/" + me
	local, err := m.Disk.Load(provider)
	if err != nil || local.RefreshToken == "" {
		if mine != nil { // logged out
			n.Delete(ctx, key)
			m.setMeta(provider, "rt", "")
			m.Logger.Info("fleet cred retracted", "provider", provider)
		}
		return
	}
	h := hash(local.RefreshToken)
	rt, _ := m.Fleet.Meta(metaKey(provider, "rt"))
	superseded, _ := m.Fleet.Meta(metaKey(provider, "superseded"))
	publish := func(epoch int) {
		e := entry{Epoch: epoch, AccessToken: local.AccessToken, ExpiresAt: local.ExpiresAt, AccountID: local.AccountID, Published: time.Now().UTC()}
		if err := n.Put(ctx, key, e); err != nil {
			m.Logger.Error("fleet cred publish", "provider", provider, "error", err)
		}
	}
	switch h {
	case superseded:
		// Our login was superseded by a newer one elsewhere; we follow.
	case rt:
		var epoch int
		if v, _ := m.Fleet.Meta(metaKey(provider, "epoch")); v != "" {
			fmt.Sscan(v, &epoch)
		}
		if best != nil && best.owner != me && best.Epoch > epoch {
			n.Delete(ctx, key)
			m.setMeta(provider, "rt", "")
			m.setMeta(provider, "superseded", h)
			m.Logger.Info("fleet cred superseded, now following", "provider", provider, "owner", best.owner, "epoch", best.Epoch)
			return
		}
		if mine == nil || mine.AccessToken != local.AccessToken {
			publish(epoch)
		}
	default: // a new login on this node: claim ownership
		epoch := 1
		if best != nil {
			epoch = best.Epoch + 1
		}
		publish(epoch)
		m.setMeta(provider, "rt", h)
		m.setMeta(provider, "epoch", fmt.Sprint(epoch))
		m.setMeta(provider, "superseded", "")
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

func metaKey(provider, k string) string { return "cred/" + provider + "/" + k }

func hash(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
