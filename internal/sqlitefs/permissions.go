// Package sqlitefs protects SQLite database files containing secrets.
package sqlitefs

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Protect creates or tightens a filesystem-backed database and existing
// sidecars to mode 0600. Call it BEFORE opening SQLite: its Unix VFS derives
// newly created WAL/journal/SHM permissions from the main file.
func Protect(dsn string) error {
	filename, _, _ := strings.Cut(dsn, "?")
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err != nil {
			return err
		}
		if u.Query().Get("mode") == "memory" || u.Path == ":memory:" || u.Opaque == ":memory:" {
			return fmt.Errorf("database must be backed by a filesystem file")
		}
		if u.Host != "" && u.Host != "localhost" || u.User != nil {
			return fmt.Errorf("invalid SQLite file URI authority")
		}
		filename = u.Path
		if u.Opaque != "" {
			filename, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return err
			}
		}
	}
	if filename == "" || filename == ":memory:" {
		return fmt.Errorf("database must be backed by a filesystem file")
	}
	if err := protectFile(filename, os.O_CREATE|os.O_RDWR); err != nil {
		return err
	}
	// Resolve symlinks to use the same sidecar filenames as SQLite.
	filename, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		name := filename + suffix
		if err := protectFile(name, os.O_RDONLY); err != nil {
			if os.IsNotExist(err) {
				// A dangling symlink is not an absent sidecar.
				if _, statErr := os.Lstat(name); os.IsNotExist(statErr) {
					continue
				} else if statErr != nil {
					return statErr
				}
			}
			return err
		}
	}
	return nil
}

func protectFile(filename string, flags int) error {
	// Do not block if a malformed database/sidecar path names a FIFO.
	file, err := os.OpenFile(filename, flags|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err == nil {
		if !info.Mode().IsRegular() {
			err = fmt.Errorf("SQLite database file is not regular: %s", filename)
		} else {
			err = file.Chmod(0o600)
		}
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
