package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Op is one entry in a node's append-only log: a last-writer-wins write (or
// delete, when Value is nil) of Key. Each node only ever appends to its own
// log; other nodes' ops are copied verbatim during sync.
type Op struct {
	Node  string          `json:"node"`
	Seq   int64           `json:"seq"`
	HLC   int64           `json:"hlc"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Entry is the current LWW-resolved value of a key.
type Entry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	HLC   int64           `json:"hlc"`
	Node  string          `json:"node"`
}

// VersionVector maps node id -> highest seq held for that node.
type VersionVector map[string]int64

// Store persists the op logs of every known node plus the folded LWW state in
// a SQLite file separate from shelley's main database.
type Store struct {
	db *sql.DB

	mu      sync.Mutex // serializes writes (appends and applies)
	lastHLC int64
}

const schema = `
CREATE TABLE IF NOT EXISTS identity (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ops (
	node TEXT NOT NULL, seq INTEGER NOT NULL, hlc INTEGER NOT NULL,
	key TEXT NOT NULL, value BLOB,
	PRIMARY KEY (node, seq)
);
CREATE TABLE IF NOT EXISTS state (
	key TEXT PRIMARY KEY, value BLOB, hlc INTEGER NOT NULL, node TEXT NOT NULL
);
`

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("fleet schema: %w", err)
	}
	s := &Store{db: db}
	if err := db.QueryRow("SELECT COALESCE(MAX(hlc),0) FROM ops").Scan(&s.lastHLC); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Identity returns the stored value for k, or "" if absent.
func (s *Store) Identity(k string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT v FROM identity WHERE k = ?", k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetIdentity(k, v string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO identity (k, v) VALUES (?, ?)", k, v)
	return err
}

// nextHLC returns a hybrid logical clock value: wall-clock milliseconds, but
// strictly greater than any HLC previously seen (locally produced or received).
// Caller holds s.mu.
func (s *Store) nextHLC() int64 {
	now := time.Now().UnixMilli()
	if now <= s.lastHLC {
		now = s.lastHLC + 1
	}
	s.lastHLC = now
	return now
}

// Append records a local write (value == nil deletes) to node's log and folds
// it into state.
func (s *Store) Append(ctx context.Context, node, key string, value json.RawMessage) (Op, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var seq int64
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM ops WHERE node = ?", node).Scan(&seq); err != nil {
		return Op{}, err
	}
	op := Op{Node: node, Seq: seq + 1, HLC: s.nextHLC(), Key: key, Value: value}
	return op, s.applyLocked(ctx, []Op{op})
}

// Apply stores ops received from peers. Ops already held (by node+seq) are
// ignored, so re-delivery is harmless.
func (s *Store) Apply(ctx context.Context, ops []Op) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range ops {
		if op.HLC > s.lastHLC {
			s.lastHLC = op.HLC
		}
	}
	return s.applyLocked(ctx, ops)
}

func (s *Store) applyLocked(ctx context.Context, ops []Op) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, op := range ops {
		res, err := tx.Exec("INSERT OR IGNORE INTO ops (node, seq, hlc, key, value) VALUES (?, ?, ?, ?, ?)",
			op.Node, op.Seq, op.HLC, op.Key, []byte(op.Value))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		// LWW: higher HLC wins, ties broken by node id.
		_, err = tx.Exec(`INSERT INTO state (key, value, hlc, node) VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, hlc = excluded.hlc, node = excluded.node
			WHERE excluded.hlc > state.hlc OR (excluded.hlc = state.hlc AND excluded.node > state.node)`,
			op.Key, []byte(op.Value), op.HLC, op.Node)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Version(ctx context.Context) (VersionVector, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT node, MAX(seq) FROM ops GROUP BY node")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vv := VersionVector{}
	for rows.Next() {
		var node string
		var seq int64
		if err := rows.Scan(&node, &seq); err != nil {
			return nil, err
		}
		vv[node] = seq
	}
	return vv, rows.Err()
}

// OpsAfter returns every op we hold that is not covered by have.
func (s *Store) OpsAfter(ctx context.Context, have VersionVector) ([]Op, error) {
	mine, err := s.Version(ctx)
	if err != nil {
		return nil, err
	}
	var out []Op
	for node, max := range mine {
		from := have[node]
		if from >= max {
			continue
		}
		rows, err := s.db.QueryContext(ctx, "SELECT node, seq, hlc, key, value FROM ops WHERE node = ? AND seq > ? ORDER BY seq", node, from)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var op Op
			var v []byte
			if err := rows.Scan(&op.Node, &op.Seq, &op.HLC, &op.Key, &v); err != nil {
				rows.Close()
				return nil, err
			}
			op.Value = v
			out = append(out, op)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get returns the live entry for key; ok is false if absent or deleted.
func (s *Store) Get(ctx context.Context, key string) (Entry, bool, error) {
	var e Entry
	var v []byte
	err := s.db.QueryRowContext(ctx, "SELECT key, value, hlc, node FROM state WHERE key = ?", key).Scan(&e.Key, &v, &e.HLC, &e.Node)
	if err == sql.ErrNoRows || (err == nil && v == nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	e.Value = v
	return e, true, nil
}

// List returns live entries whose key starts with prefix, ordered by key.
func (s *Store) List(ctx context.Context, prefix string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value, hlc, node FROM state WHERE key >= ? AND key < ? AND value IS NOT NULL ORDER BY key",
		prefix, prefix+"\uffff")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var v []byte
		if err := rows.Scan(&e.Key, &v, &e.HLC, &e.Node); err != nil {
			return nil, err
		}
		e.Value = v
		out = append(out, e)
	}
	return out, rows.Err()
}
