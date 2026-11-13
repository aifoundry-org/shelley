package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrLegacyClaimed = errors.New("legacy subscription credentials are bound to another database; sign in again instead")

// LegacyImport is explicitly enabled by production server setup, never by a
// DBStore constructor. Import must only be requested after stopping any older
// Shelley servers that still use the JSON file.
type LegacyImport struct {
	Store *DBStore
	Path  string
}

type legacyClaim struct {
	StoreID  string   `json:"store_id"`
	Database string   `json:"database"`
	Digest   [32]byte `json:"digest"`
}

// Available checks metadata only: it never loads or imports credential data.
// A claim for a different DB is not an available import.
func (m *LegacyImport) Available(ctx context.Context) (bool, error) {
	id, done, err := m.Store.DB.SubscriptionCredentialStore(ctx)
	if err != nil || done {
		return false, err
	}
	path, err := filepath.EvalSymlinks(m.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	claim, err := readLegacyClaim(path + ".sqlite-owner")
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return claim.StoreID == id && claim.Database == m.Store.DB.Path(), nil
}

func readLegacyClaim(path string) (legacyClaim, error) {
	var claim legacyClaim
	raw, err := os.ReadFile(path)
	if err != nil {
		return claim, err
	}
	if err := json.Unmarshal(raw, &claim); err != nil || claim.StoreID == "" || claim.Database == "" {
		return claim, fmt.Errorf("invalid legacy subscription import ownership record")
	}
	return claim, nil
}

// Import reserves the legacy file for exactly one database, then atomically
// commits tokens and a completion marker. The original file remains unchanged.
// A crash after the reservation is recoverable only by the same database.
func (m *LegacyImport) Import(ctx context.Context) error {
	id, done, err := m.Store.DB.SubscriptionCredentialStore(ctx)
	if err != nil || done {
		return err
	}
	path, err := filepath.EvalSymlinks(m.Path)
	if err != nil {
		return fmt.Errorf("locate legacy subscription credentials: %w", err)
	}
	lock, err := os.OpenFile(path+".sqlite-import.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open legacy subscription import lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another legacy subscription import is in progress: %w", err)
	}
	// Recheck after locking: another request may already have completed.
	_, done, err = m.Store.DB.SubscriptionCredentialStore(ctx)
	if err != nil || done {
		return err
	}
	claimPath := path + ".sqlite-owner"
	claim, claimErr := readLegacyClaim(claimPath)
	if claimErr != nil && !os.IsNotExist(claimErr) {
		return claimErr
	}
	if claimErr == nil && (claim.StoreID != id || claim.Database != m.Store.DB.Path()) {
		return ErrLegacyClaimed
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read legacy subscription credentials: %w", err)
	}
	digest := sha256.Sum256(data)
	if claimErr == nil && digest != claim.Digest {
		return fmt.Errorf("legacy subscription credentials changed after import was reserved; stop older servers and sign in again")
	}
	var tokens map[string]Token
	if err := json.Unmarshal(data, &tokens); err != nil || tokens == nil {
		return fmt.Errorf("parse legacy subscription credentials: invalid credential data")
	}
	rawTokens := make(map[string]string, len(tokens))
	for provider, tok := range tokens {
		raw, err := json.Marshal(tok)
		if err != nil {
			return fmt.Errorf("encode legacy subscription credentials")
		}
		rawTokens[provider] = string(raw)
	}
	if os.IsNotExist(claimErr) {
		claim = legacyClaim{StoreID: id, Database: m.Store.DB.Path(), Digest: digest}
		if err := writeLegacyClaim(claimPath, claim); err != nil {
			return err
		}
	} else if err := syncLegacyClaim(claimPath); err != nil {
		// A previous attempt may have failed after rename but before the
		// directory sync. Never commit tokens without a durable reservation.
		return err
	}
	return m.Store.DB.ImportSubscriptionCredentials(ctx, rawTokens)
}

// Publish the non-secret ownership receipt durably BEFORE committing tokens.
// The persistent flock inode is never renamed or removed.
func writeLegacyClaim(path string, claim legacyClaim) error {
	raw, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".subscription-import-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncLegacyClaim(path)
}

func syncLegacyClaim(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
