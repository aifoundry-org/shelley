package fleet

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// Transport moves bytes between fleet nodes. Each node listens once and dials
// peers by the address they publish in the roster. The tailcat implementation
// is used in production; loopbackTransport runs multi-node tests in-process.
type Transport interface {
	// ID is this node's identity, as peers will see it.
	ID() string
	// Addr is the dialable address this node publishes to the roster.
	Addr() string
	Listener() net.Listener
	Dial(ctx context.Context, addr string) (net.Conn, error)
	Close() error
}

// fleetPort is the in-tunnel TCP port the sync HTTP server listens on.
const fleetPort = 7

type tailcatTransport struct {
	srv    *tailcat.Server
	ln     net.Listener
	addr   tailcat.Addr // without PSK: what the roster publishes
	invite tailcat.Addr // with PSK: what joiners need
	psk    tailcat.PresharedKey
	nodeID string
	logf   logger.Logf

	mu      sync.Mutex
	clients map[tailcat.Addr]*tailcat.Client
}

func newTailcatTransport(ctx context.Context, nk key.NodePrivate, psk tailcat.PresharedKey, logf logger.Logf) (*tailcatTransport, error) {
	t := &tailcatTransport{
		psk:     psk,
		nodeID:  nk.Public().String(),
		logf:    logf,
		clients: map[tailcat.Addr]*tailcat.Client{},
	}
	t.srv = &tailcat.Server{Key: nk, PresharedKey: t.psk, Logf: logf}
	if err := t.srv.Start(); err != nil {
		return nil, fmt.Errorf("tailcat start: %w", err)
	}
	ln, err := t.srv.Listen(ctx, "tcp", fmt.Sprintf(":%d", fleetPort))
	if err != nil {
		t.srv.Close()
		return nil, err
	}
	t.ln = ln
	t.invite = t.srv.TailcatAddr()
	addr, _, err := splitInvite(string(t.invite))
	if err != nil {
		t.Close()
		return nil, err
	}
	t.addr = addr
	return t, nil
}

// splitInvite separates a tailcat address carrying the fleet PSK into the
// public address (what peers publish) and the PSK (what gates the fleet).
func splitInvite(invite string) (tailcat.Addr, tailcat.PresharedKey, error) {
	ci, err := tailcat.ParseAddr(tailcat.Addr(invite))
	if err != nil {
		return "", tailcat.PresharedKey{}, err
	}
	if ci.PresharedKey.IsZero() {
		return "", tailcat.PresharedKey{}, fmt.Errorf("invite carries no pre-shared key")
	}
	psk := ci.PresharedKey
	ci.PresharedKey = tailcat.PresharedKey{}
	return ci.Addr(), psk, nil
}

func (t *tailcatTransport) ID() string             { return t.nodeID }
func (t *tailcatTransport) Addr() string           { return string(t.addr) }
func (t *tailcatTransport) Listener() net.Listener { return t.ln }

func (t *tailcatTransport) client(addr tailcat.Addr) (*tailcat.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.clients[addr]; ok {
		return c, nil
	}
	ci, err := tailcat.ParseAddr(addr)
	if err != nil {
		return nil, err
	}
	ci.PresharedKey = t.psk
	// The client must NOT reuse the server's node key: each tailcat Client
	// has its own magicsock and DERP connection, and DERP treats two live
	// connections under one key as a cloned key and drops traffic to both.
	// Identity is the server key; clients are anonymous.
	c := &tailcat.Client{Server: ci.Addr(), Logf: t.logf}
	t.clients[addr] = c
	return c, nil
}

func (t *tailcatTransport) Dial(ctx context.Context, addr string) (net.Conn, error) {
	c, err := t.client(tailcat.Addr(addr))
	if err != nil {
		return nil, err
	}
	return c.DialTCPPort(ctx, fleetPort)
}

func (t *tailcatTransport) Close() error {
	t.mu.Lock()
	for _, c := range t.clients {
		c.Close()
	}
	t.mu.Unlock()
	t.ln.Close()
	return t.srv.Close()
}

// loopbackTransport is a plain-TCP transport on localhost for tests.
type loopbackTransport struct {
	id string
	ln net.Listener
}

func newLoopbackTransport(id string) (*loopbackTransport, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &loopbackTransport{id: id, ln: ln}, nil
}

func (t *loopbackTransport) ID() string             { return t.id }
func (t *loopbackTransport) Addr() string           { return t.ln.Addr().String() }
func (t *loopbackTransport) Listener() net.Listener { return t.ln }
func (t *loopbackTransport) Close() error           { return t.ln.Close() }
func (t *loopbackTransport) Dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}
