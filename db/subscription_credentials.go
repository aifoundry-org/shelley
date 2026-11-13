package db

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"
)

func (db *DB) LoadSubscriptionCredential(ctx context.Context, provider string) (string, error) {
	var raw string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		token, err := generated.New(rx.Conn()).GetSubscriptionCredential(ctx, provider)
		if err != nil {
			return err
		}
		if token == nil {
			return sql.ErrNoRows
		}
		raw = *token
		return nil
	})
	return raw, err
}

func (db *DB) SaveSubscriptionCredential(ctx context.Context, provider, raw string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.SaveSubscriptionCredential(ctx, generated.SaveSubscriptionCredentialParams{Provider: provider, Token: &raw})
	})
}

func (db *DB) DeleteSubscriptionCredential(ctx context.Context, provider string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.DeleteSubscriptionCredential(ctx, provider)
	})
}

func (db *DB) RefreshSubscriptionCredential(ctx context.Context, provider, previous, raw string) (bool, error) {
	var changed int64
	err := db.QueriesTx(ctx, func(q *generated.Queries) error {
		var err error
		changed, err = q.RefreshSubscriptionCredential(ctx, generated.RefreshSubscriptionCredentialParams{
			Provider: provider, Previous: &previous, Token: &raw,
		})
		return err
	})
	return changed != 0, err
}

// SubscriptionCredentialStore identifies this database independently of its
// filename, so replacing a DB cannot reimport an already claimed token family.
func (db *DB) SubscriptionCredentialStore(ctx context.Context) (id string, imported bool, err error) {
	err = db.QueriesTx(ctx, func(q *generated.Queries) error {
		if err := q.InitSubscriptionCredentialStore(ctx, uuid.NewString()); err != nil {
			return err
		}
		row, err := q.GetSubscriptionCredentialStore(ctx)
		id, imported = row.StoreID, row.LegacyImported != 0
		return err
	})
	return
}

// ImportSubscriptionCredentials commits credentials and the one-time marker
// together. Existing credentials AND logout tombstones always take precedence.
func (db *DB) ImportSubscriptionCredentials(ctx context.Context, tokens map[string]string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		row, err := q.GetSubscriptionCredentialStore(ctx)
		if err != nil || row.LegacyImported != 0 {
			return err
		}
		for provider, raw := range tokens {
			if err := q.ImportSubscriptionCredential(ctx, generated.ImportSubscriptionCredentialParams{Provider: provider, Token: &raw}); err != nil {
				return err
			}
		}
		return q.CompleteSubscriptionCredentialImport(ctx)
	})
}
