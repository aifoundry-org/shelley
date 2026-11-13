package oauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func legacyFixture(t *testing.T) (string, Token) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	token := Token{AccessToken: "legacy-access", RefreshToken: "legacy-refresh", AccountID: "legacy-account", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
	if err := (&Store{Path: path}).Save("openai", token); err != nil {
		t.Fatal(err)
	}
	return path, token
}

func TestLegacyImportExplicitAndOneTime(t *testing.T) {
	path, token := legacyFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := testDBStore(t)
	importer := &LegacyImport{Store: store, Path: path}
	if _, err := store.Load("openai"); err == nil {
		t.Fatal("opening a store implicitly imported credentials")
	}
	if available, err := importer.Available(context.Background()); err != nil || !available {
		t.Fatal("import should be available", err)
	}
	if _, err := store.Load("openai"); err == nil {
		t.Fatal("availability check imported credentials")
	}
	if err := importer.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load("openai"); err != nil || got != token {
		t.Fatal("import did not preserve complete token", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("import altered the legacy backup", err)
	}
	if err := store.Delete("openai"); err != nil {
		t.Fatal(err)
	}
	dbPath := store.DB.Path()
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	store = openDBStore(t, dbPath)
	importer.Store = store
	if available, err := importer.Available(context.Background()); err != nil || available {
		t.Fatal("completed import was advertised again", err)
	}
	if err := importer.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("openai"); err == nil {
		t.Fatal("restart/import resurrected a logged-out token")
	}
	other := &LegacyImport{Store: testDBStore(t), Path: path}
	if available, err := other.Available(context.Background()); err != nil || available {
		t.Fatal("another DB advertised an unsafe import", err)
	}
	if err := other.Import(context.Background()); !errors.Is(err, ErrLegacyClaimed) {
		t.Fatal("another DB was not refused", err)
	}
}

func TestLegacyImportPreservesNewerCredentialsAndLogout(t *testing.T) {
	for _, loggedOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "newer login", true: "logout"}[loggedOut], func(t *testing.T) {
			path, _ := legacyFixture(t)
			store := testDBStore(t)
			fresh := Token{AccessToken: "fresh", RefreshToken: "new-family"}
			if err := store.Save("openai", fresh); err != nil {
				t.Fatal(err)
			}
			if loggedOut {
				if err := store.Delete("openai"); err != nil {
					t.Fatal(err)
				}
			}
			if err := (&LegacyImport{Store: store, Path: path}).Import(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := store.Load("openai")
			if loggedOut && err == nil {
				t.Fatal("import undid an earlier logout")
			}
			if !loggedOut && (err != nil || got != fresh) {
				t.Fatal("import overwrote newer credentials", err)
			}
		})
	}
}

func TestLegacyImportErrors(t *testing.T) {
	for _, contents := range []string{"{", "null", `{"openai":{"expires_at":"secret-token-must-not-leak"}}`} {
		t.Run(contents, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			importer := &LegacyImport{Store: testDBStore(t), Path: path}
			if err := importer.Import(context.Background()); err == nil || strings.Contains(err.Error(), "secret-token") {
				t.Fatal("parse error missing or leaking credentials")
			}
			if _, err := os.Stat(path + ".sqlite-owner"); !os.IsNotExist(err) {
				t.Fatal("invalid file was claimed")
			}
		})
	}
	importer := &LegacyImport{Store: testDBStore(t), Path: t.TempDir()} // read directory fails
	if err := importer.Import(context.Background()); err == nil {
		t.Fatal("read failure was ignored")
	}
	importer.Path = filepath.Join(t.TempDir(), "missing.json")
	if available, err := importer.Available(context.Background()); err != nil || available {
		t.Fatal("absent file advertised", err)
	}
	if err := importer.Import(context.Background()); err == nil {
		t.Fatal("missing requested import silently succeeded")
	}
}

func TestLegacyImportCrashRecovery(t *testing.T) {
	path, token := legacyFixture(t)
	store := testDBStore(t)
	id, _, err := store.DB.SubscriptionCredentialStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after durable reservation, before the DB transaction.
	if err := writeLegacyClaim(path+".sqlite-owner", legacyClaim{StoreID: id, Database: store.DB.Path(), Digest: sha256.Sum256(raw)}); err != nil {
		t.Fatal(err)
	}
	// A failed DB write after the receipt must leave neither credentials nor
	// the completion marker behind, and must not release the reservation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.DB.ImportSubscriptionCredentials(ctx, map[string]string{"openai": string(raw)}); !errors.Is(err, context.Canceled) {
		t.Fatal("expected canceled import transaction", err)
	}
	if _, err := store.Load("openai"); !errors.Is(err, ErrNoCredentials) {
		t.Fatal("failed transaction imported a credential", err)
	}
	if _, done, err := store.DB.SubscriptionCredentialStore(context.Background()); err != nil || done {
		t.Fatal("failed transaction committed the import marker", err)
	}
	dbPath := store.DB.Path()
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	store = openDBStore(t, dbPath)
	if err := (&LegacyImport{Store: testDBStore(t), Path: path}).Import(context.Background()); !errors.Is(err, ErrLegacyClaimed) {
		t.Fatal("reservation did not exclude other database", err)
	}
	if err := (&LegacyImport{Store: store, Path: path}).Import(context.Background()); err != nil {
		t.Fatal("same database could not recover", err)
	}
	if got, err := store.Load("openai"); err != nil || got != token {
		t.Fatal("recovery lost token", err)
	}
}

func TestLegacyImportConcurrentDatabases(t *testing.T) {
	path, _ := legacyFixture(t)
	importers := []*LegacyImport{{Store: testDBStore(t), Path: path}, {Store: testDBStore(t), Path: path}}
	var wg sync.WaitGroup
	results := make(chan error, len(importers))
	for _, importer := range importers {
		wg.Go(func() { results <- importer.Import(context.Background()) })
	}
	wg.Wait()
	successes := 0
	for range importers {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrLegacyClaimed) && !errors.Is(err, unix.EWOULDBLOCK) {
			t.Fatal("unexpected losing-import error", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful imports = %d, want 1", successes)
	}
	loaded := 0
	for _, importer := range importers {
		if _, err := importer.Store.Load("openai"); err == nil {
			loaded++
		}
	}
	if loaded != 1 {
		t.Fatalf("credential owners = %d, want exactly 1", loaded)
	}
}

func TestLegacyImportCanonicalPathAliases(t *testing.T) {
	path, token := legacyFixture(t)
	store := testDBStore(t)
	legacyAlias := filepath.Join(t.TempDir(), "credentials-alias.json")
	dbAlias := filepath.Join(t.TempDir(), "database-alias.db")
	if err := os.Symlink(path, legacyAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store.DB.Path(), dbAlias); err != nil {
		t.Fatal(err)
	}
	aliasStore := openDBStore(t, dbAlias)
	if aliasStore.DB.Path() != store.DB.Path() {
		t.Fatal("database alias did not resolve to the canonical path")
	}
	if err := (&LegacyImport{Store: aliasStore, Path: legacyAlias}).Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load("openai"); err != nil || got != token {
		t.Fatal("alias import did not reach the original database", err)
	}
	if _, err := os.Stat(path + ".sqlite-owner"); err != nil {
		t.Fatal("receipt was not placed beside the canonical legacy file", err)
	}
	other := &LegacyImport{Store: testDBStore(t), Path: path}
	if err := other.Import(context.Background()); !errors.Is(err, ErrLegacyClaimed) {
		t.Fatal("canonical legacy path bypassed alias reservation", err)
	}
}

func TestLegacyImportRejectsChangedReservation(t *testing.T) {
	path, _ := legacyFixture(t)
	store := testDBStore(t)
	id, _, err := store.DB.SubscriptionCredentialStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLegacyClaim(path+".sqlite-owner", legacyClaim{StoreID: id, Database: store.DB.Path()}); err != nil {
		t.Fatal(err)
	}
	if err := (&LegacyImport{Store: store, Path: path}).Import(context.Background()); err == nil {
		t.Fatal("changed token family was imported after reservation")
	}
}
