package db

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseFilesPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	open := func() *DB {
		t.Helper()
		database, err := New(Config{DSN: path})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		return database
	}
	check := func() {
		t.Helper()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			info, err := os.Stat(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %o, want 600", suffix, info.Mode().Perm())
			}
		}
	}
	database := open()
	check()
	// Reopening an old installation tightens existing files before SQLite
	// opens them, including when a different connection still has WAL open.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := open()
	check()
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	// Closing the last connection removes sidecars; newly created sidecars
	// must also be private despite the process's ordinary umask.
	database = open()
	defer database.Close()
	check()
}

func TestDatabasePermissionsFileURIAndExistingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private # and ?.db")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rwc"}).String()
	database, err := New(Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other, err := New(Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("URI sidecar %q not secured: %v", suffix, err)
		}
	}
	// Exercise a preexisting rollback journal without asking SQLite to
	// interpret a fabricated journal.
	if err := os.WriteFile(path+"-journal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := protectDatabaseFiles(dsn); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + "-journal")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("existing rollback journal was not secured", err)
	}
}
