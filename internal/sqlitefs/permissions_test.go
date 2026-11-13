package sqlitefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProtectRejectsNonregularFilesWithoutChmod(t *testing.T) {
	for _, position := range []string{"main", "sidecar"} {
		for _, kind := range []string{"directory", "fifo"} {
			t.Run(position+"/"+kind, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "private.db")
				target := path
				if position == "sidecar" {
					target += "-wal"
				}
				var err error
				if kind == "directory" {
					err = os.Mkdir(target, 0o755)
				} else {
					err = syscall.Mkfifo(target, 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if err := Protect(path); err == nil {
					t.Fatal("nonregular SQLite file was accepted")
				}
				after, err := os.Stat(target)
				if err != nil || before.Mode() != after.Mode() {
					t.Fatal("nonregular SQLite file was chmodded", err)
				}
			})
		}
	}
}

func TestProtectRejectsDanglingSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	if err := os.Symlink("missing-file", path+"-wal"); err != nil {
		t.Fatal(err)
	}
	if err := Protect(path); err == nil {
		t.Fatal("dangling sidecar was treated as an absent sidecar")
	}
}
