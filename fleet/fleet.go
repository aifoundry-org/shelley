// Package fleet replicates a small key/value state across every shelley in a
// fleet, peer to peer, with no leader and no quorum.
//
// Each node appends to its own log only; peers exchange version vectors and
// copy the ops they lack (anti-entropy). State is the last-writer-wins fold of
// all logs. Nodes learn about each other through the state itself: every node
// writes "node/<id>" with its address, so one seed address is enough to find
// the whole fleet.
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NodeInfo is the value stored under "node/<id>".
type NodeInfo struct {
	Name string    `json:"name"`
	Addr string    `json:"addr"`
	Seen time.Time `json:"seen"`
}

// Peer is a fleet member as seen by this node.
type Peer struct {
	ID string `json:"id"`
	NodeInfo
	LastSync time.Time `json:"last_sync,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type Node struct {
	name   string
	store  *Store
	tr     Transport
	logger *slog.Logger
	httpc  *http.Client
	cancel context.CancelFunc
	done   chan struct{}
	kick   chan struct{}

	mu    sync.Mutex
	peers map[string]*Peer // by addr; seeds + roster
}

const (
	syncInterval      = 10 * time.Second
	heartbeatInterval = 2 * time.Minute
	fanout            = 3
)

// start brings up a node on an open store and transport. The caller owns the
// store; Close stops the node but leaves the store open.
func start(ctx context.Context, name string, store *Store, tr Transport, logger *slog.Logger) (*Node, error) {
	ctx, cancel := context.WithCancel(ctx)
	n := &Node{
		name: name, store: store, tr: tr, logger: logger.With("component", "fleet"),
		cancel: cancel, done: make(chan struct{}), kick: make(chan struct{}, 1),
		peers: map[string]*Peer{},
	}
	n.httpc = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return tr.Dial(ctx, strings.TrimSuffix(addr, ":80"))
			},
		},
	}
	if err := n.heartbeat(ctx); err != nil {
		return nil, err
	}
	n.loadRoster(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sync", n.handleSync)
	go http.Serve(tr.Listener(), mux)
	go n.run(ctx)
	n.logger.Info("fleet node started", "id", n.ID(), "name", name, "addr", tr.Addr())
	return n, nil
}

func (n *Node) ID() string   { return n.tr.ID() }
func (n *Node) Addr() string { return n.tr.Addr() }

func (n *Node) Close() error {
	n.cancel()
	<-n.done
	return n.tr.Close()
}

// Put writes key locally and nudges the sync loop so peers see it promptly.
func (n *Node) Put(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := n.store.Append(ctx, n.ID(), key, b); err != nil {
		return err
	}
	n.Kick()
	return nil
}

func (n *Node) Delete(ctx context.Context, key string) error {
	if _, err := n.store.Append(ctx, n.ID(), key, nil); err != nil {
		return err
	}
	n.Kick()
	return nil
}

func (n *Node) Get(ctx context.Context, key string) (Entry, bool, error) {
	return n.store.Get(ctx, key)
}
func (n *Node) List(ctx context.Context, prefix string) ([]Entry, error) {
	return n.store.List(ctx, prefix)
}

// Join adds a peer by address and syncs with it. Once the sync succeeds the
// roster is persisted, so a join survives restarts.
func (n *Node) Join(ctx context.Context, addr string) error {
	if addr == n.Addr() {
		return errors.New("fleet: cannot join self")
	}
	n.mu.Lock()
	if _, ok := n.peers[addr]; !ok {
		n.peers[addr] = &Peer{NodeInfo: NodeInfo{Addr: addr}}
	}
	n.mu.Unlock()
	if err := n.syncWith(ctx, addr); err != nil {
		n.mu.Lock()
		delete(n.peers, addr)
		n.mu.Unlock()
		return err
	}
	n.loadRoster(ctx)
	n.Kick()
	return nil
}

// Kick requests an immediate sync round.
func (n *Node) Kick() {
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

func (n *Node) Peers() []Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Peer, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, *p)
	}
	return out
}

func (n *Node) heartbeat(ctx context.Context) error {
	return n.Put(ctx, "node/"+n.ID(), NodeInfo{Name: n.name, Addr: n.Addr(), Seen: time.Now().UTC()})
}

// loadRoster rebuilds the peer set from the live "node/*" entries. Peers with
// no ID yet (a Join in flight) are kept until the roster names them.
func (n *Node) loadRoster(ctx context.Context) {
	entries, err := n.store.List(ctx, "node/")
	if err != nil {
		n.logger.Error("fleet roster", "error", err)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	peers := map[string]*Peer{}
	for addr, p := range n.peers {
		if p.ID == "" {
			peers[addr] = p
		}
	}
	for _, e := range entries {
		id := strings.TrimPrefix(e.Key, "node/")
		if id == n.ID() {
			continue
		}
		var info NodeInfo
		if err := json.Unmarshal(e.Value, &info); err != nil {
			continue
		}
		p, ok := n.peers[info.Addr]
		if !ok {
			p = &Peer{}
		}
		p.ID = id
		p.NodeInfo = info
		peers[info.Addr] = p
	}
	n.peers = peers
}

func (n *Node) run(ctx context.Context) {
	defer close(n.done)
	sync := time.NewTicker(syncInterval)
	defer sync.Stop()
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	n.syncRound(ctx, len(n.Peers())) // first round: everyone we know
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if err := n.heartbeat(ctx); err != nil {
				n.logger.Error("fleet heartbeat", "error", err)
			}
		case <-sync.C:
			n.syncRound(ctx, fanout)
		case <-n.kick:
			n.syncRound(ctx, len(n.Peers()))
		}
	}
}

// syncRound syncs with up to k randomly chosen peers.
func (n *Node) syncRound(ctx context.Context, k int) {
	peers := n.Peers()
	rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })
	if k < len(peers) {
		peers = peers[:k]
	}
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := n.syncWith(ctx, p.Addr)
			n.mu.Lock()
			if q, ok := n.peers[p.Addr]; ok {
				if err != nil {
					q.Error = err.Error()
				} else {
					q.Error = ""
					q.LastSync = time.Now().UTC()
				}
			}
			n.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				n.logger.Warn("fleet sync failed", "peer", p.ID, "name", p.Name, "error", err)
			}
		}()
	}
	wg.Wait()
	n.loadRoster(ctx)
}

type syncMsg struct {
	Have VersionVector `json:"have"`
	Ops  []Op          `json:"ops,omitempty"`
}

// syncWith does a pull then a push against one peer: the peer replies with
// what we lack, then we send what it lacks.
func (n *Node) syncWith(ctx context.Context, addr string) error {
	have, err := n.store.Version(ctx)
	if err != nil {
		return err
	}
	resp, err := n.exchange(ctx, addr, syncMsg{Have: have})
	if err != nil {
		return err
	}
	if err := n.store.Apply(ctx, resp.Ops); err != nil {
		return err
	}
	ops, err := n.store.OpsAfter(ctx, resp.Have)
	if err != nil || len(ops) == 0 {
		return err
	}
	have, err = n.store.Version(ctx)
	if err != nil {
		return err
	}
	resp, err = n.exchange(ctx, addr, syncMsg{Have: have, Ops: ops})
	if err != nil {
		return err
	}
	return n.store.Apply(ctx, resp.Ops)
}

func (n *Node) exchange(ctx context.Context, addr string, msg syncMsg) (syncMsg, error) {
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+addr+"/sync", bytes.NewReader(body))
	if err != nil {
		return syncMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := n.httpc.Do(req)
	if err != nil {
		return syncMsg{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return syncMsg{}, fmt.Errorf("sync: %s", res.Status)
	}
	var out syncMsg
	return out, json.NewDecoder(res.Body).Decode(&out)
}

// handleSync serves the peer side of syncWith over the transport.
func (n *Node) handleSync(w http.ResponseWriter, r *http.Request) {
	var msg syncMsg
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := n.store.Apply(ctx, msg.Ops); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ops, err := n.store.OpsAfter(ctx, msg.Have)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	have, err := n.store.Version(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(syncMsg{Have: have, Ops: ops})
	if len(msg.Ops) > 0 {
		n.loadRoster(ctx)
	}
}
