package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openPermissionStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func checkStorePermissions(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("fleet database %q mode = %o, want 600", suffix, info.Mode().Perm())
		}
	}
}

func TestStoreFilesPrivateAndReopenPreservesSecrets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fleet.db")
	first := openPermissionStore(t, path)
	checkStorePermissions(t, path)
	if err := first.SetIdentity("psk", "dummy-fleet-psk"); err != nil {
		t.Fatal(err)
	}
	key := "cred/anthropic/owner"
	if _, err := first.Append(ctx, "owner", key, json.RawMessage(`{"access_token":"dummy-access-token","epoch":1}`)); err != nil {
		t.Fatal(err)
	}
	// Retraction leaves the historical bearer token in the ops log.
	if _, err := first.Append(ctx, "owner", key, nil); err != nil {
		t.Fatal(err)
	}
	before, err := first.OpsAfter(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second := openPermissionStore(t, path)
	checkStorePermissions(t, path)
	if psk, err := second.Identity("psk"); err != nil || psk != "dummy-fleet-psk" {
		t.Fatal("tightening permissions changed fleet identity", err)
	}
	after, err := second.OpsAfter(ctx, nil)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("tightening permissions changed historical credential operations", err)
	}
	if _, ok, err := second.Get(ctx, key); err != nil || ok {
		t.Fatal("tightening permissions changed credential retraction", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatal("last close did not remove fleet sidecar", err)
		}
	}
	reopened := openPermissionStore(t, path)
	checkStorePermissions(t, path)
	after, err = reopened.OpsAfter(ctx, nil)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("private sidecar recreation changed historical operations", err)
	}
}

func TestStoreExistingJournalTightenedBeforeSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.db")
	openPermissionStore(t, path)
	if err := os.WriteFile(path+"-journal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	journal, err := os.Open(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	openPermissionStore(t, path)
	// SQLite may remove an empty journal; the already-open fd still proves
	// it was tightened before SQLite interpreted or removed that file.
	info, err := journal.Stat()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("existing fleet rollback journal was not secured", err)
	}
}

func TestStoreSymlinkProtectsActualSidecars(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "fleet.db")
	original := openPermissionStore(t, real)
	if err := original.SetIdentity("psk", "dummy-fleet-psk"); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.db")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(real+suffix, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reopened := openPermissionStore(t, alias)
	checkStorePermissions(t, real)
	if psk, err := reopened.Identity("psk"); err != nil || psk != "dummy-fleet-psk" {
		t.Fatal("symlink reopen changed fleet identity", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(alias + suffix); !os.IsNotExist(err) {
			t.Fatal("symlink reopen created sidecars at the wrong path", err)
		}
	}
}

func TestStorePermissionFailurePrecedesSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.db")
	// A regular file cannot be traversed as a directory, regardless of UID.
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(blocker, "wal"), path+"-wal"); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if store != nil {
		store.Close()
		t.Fatal("fleet opened despite sidecar permission failure")
	}
	if err == nil || !strings.Contains(err.Error(), "secure fleet database permissions") {
		t.Fatal("fleet did not propagate the pre-SQLite permission failure", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Fatal("schema was written before securing fleet files", err)
	}
}

func TestStoreRejectsSidecarDirectoryWithoutChangingItsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.db")
	sidecar := path + "-wal"
	if err := os.Mkdir(sidecar, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if store != nil {
		store.Close()
		t.Fatal("fleet opened with a sidecar directory")
	}
	if err == nil || !strings.Contains(err.Error(), "secure fleet database permissions") {
		t.Fatal("fleet did not reject the sidecar directory before SQLite", err)
	}
	after, err := os.Stat(sidecar)
	if err != nil || after.Mode().Perm() != before.Mode().Perm() {
		t.Fatal("protecting fleet database chmodded a sidecar directory", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Fatal("schema was written with an invalid sidecar directory", err)
	}
}

func TestLeaveRecreatesPrivateStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fleet.db")
	store := openPermissionStore(t, path)
	tr, err := newLoopbackTransport("owner")
	if err != nil {
		t.Fatal(err)
	}
	n, err := start(ctx, "owner", store, tr, slog.Default())
	if err != nil {
		tr.Close()
		t.Fatal(err)
	}
	service := &Service{ctx: ctx, path: path, logger: slog.Default(), store: store, node: n}
	t.Cleanup(func() { service.Close() })
	if err := store.SetIdentity("psk", "dummy-fleet-psk"); err != nil {
		t.Fatal(err)
	}
	if err := n.Put(ctx, "cred/anthropic/owner", map[string]string{"access_token": "dummy-access-token"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	checkStorePermissions(t, path)
	if psk, err := service.store.Identity("psk"); err != nil || psk != "" {
		t.Fatal("Leave retained old fleet identity", err)
	}
	if ops, err := service.store.OpsAfter(ctx, nil); err != nil || len(ops) != 0 {
		t.Fatal("Leave retained old fleet operations", err)
	}
}
