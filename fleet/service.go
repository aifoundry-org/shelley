package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

// ErrNotJoined is returned by node operations when this shelley is not in a
// fleet.
var ErrNotJoined = errors.New("fleet: not in a fleet (run shelley fleet init or join)")

// Service owns the fleet store for one shelley and the node, if the shelley
// is in a fleet. Membership is state, not config: Init creates a fleet,
// Join enters one with an invite, Leave forgets everything.
type Service struct {
	ctx    context.Context
	path   string
	logger *slog.Logger

	mu    sync.Mutex
	store *Store
	node  *Node
}

// Open opens the store at path and, if it holds a fleet identity, starts the
// node.
func Open(ctx context.Context, path string, logger *slog.Logger) (*Service, error) {
	store, err := OpenStore(path)
	if err != nil {
		return nil, err
	}
	s := &Service{ctx: ctx, path: path, logger: logger, store: store}
	psk, err := store.Identity("psk")
	if err != nil {
		store.Close()
		return nil, err
	}
	if psk != "" {
		if err := s.startLocked(); err != nil {
			store.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node != nil {
		s.node.Close()
		s.node = nil
	}
	return s.store.Close()
}

// Node returns the running node or ErrNotJoined.
func (s *Service) Node() (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node == nil {
		return nil, ErrNotJoined
	}
	return s.node, nil
}

// Init creates a new fleet with this node as its first member.
func (s *Service) Init(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node != nil {
		return errors.New("fleet: already in a fleet")
	}
	return s.enroll(name, tailcat.NewPresharedKey())
}

// Join enters the fleet of the node that issued invite.
func (s *Service) Join(ctx context.Context, name, invite string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	addr, psk, err := splitInvite(invite)
	if err != nil {
		return fmt.Errorf("fleet: bad invite: %w", err)
	}
	if s.node == nil {
		if err := s.enroll(name, psk); err != nil {
			return err
		}
	}
	return s.node.Join(ctx, string(addr))
}

// Invite returns the token another shelley needs to join this fleet.
func (s *Service) Invite() (string, error) {
	n, err := s.Node()
	if err != nil {
		return "", err
	}
	return string(n.tr.(*tailcatTransport).invite), nil
}

// Leave stops the node and discards identity and state.
func (s *Service) Leave(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node == nil {
		return ErrNotJoined
	}
	// Retract our roster entry so peers forget us, then stop.
	n := s.node
	if _, err := n.store.Append(ctx, n.ID(), "node/"+n.ID(), nil); err != nil {
		return err
	}
	n.syncRound(ctx, len(n.Peers()))
	n.Close()
	s.node = nil
	s.store.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(s.path + suffix)
	}
	store, err := OpenStore(s.path)
	if err != nil {
		return err
	}
	s.store = store
	return nil
}

// enroll stores a fresh identity and starts the node. Caller holds s.mu.
func (s *Service) enroll(name string, psk tailcat.PresharedKey) error {
	if name == "" {
		name, _ = os.Hostname()
	}
	nk := key.NewNode()
	nkText, _ := nk.MarshalText()
	pskText, _ := psk.MarshalText()
	for k, v := range map[string]string{"nodekey": string(nkText), "psk": string(pskText), "name": name} {
		if err := s.store.SetIdentity(k, v); err != nil {
			return err
		}
	}
	return s.startLocked()
}

func (s *Service) startLocked() error {
	var nk key.NodePrivate
	var psk tailcat.PresharedKey
	for k, v := range map[string]interface{ UnmarshalText([]byte) error }{"nodekey": &nk, "psk": &psk} {
		text, err := s.store.Identity(k)
		if err != nil {
			return err
		}
		if err := v.UnmarshalText([]byte(text)); err != nil {
			return fmt.Errorf("fleet identity %s: %w", k, err)
		}
	}
	name, err := s.store.Identity("name")
	if err != nil {
		return err
	}
	logf := func(format string, args ...any) { s.logger.Debug("tailcat: " + fmt.Sprintf(format, args...)) }
	tr, err := newTailcatTransport(s.ctx, nk, psk, logf)
	if err != nil {
		return err
	}
	n, err := start(s.ctx, name, s.store, tr, s.logger)
	if err != nil {
		tr.Close()
		return err
	}
	s.node = n
	return nil
}
