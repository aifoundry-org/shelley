package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"shelley.exe.dev/db"
)

// DBStore keeps complete OAuth tokens local to their owner in Shelley's main
// database. Fleet replication must only publish short-lived access tokens.
type DBStore struct {
	DB *db.DB
}

var _ Credentials = (*DBStore)(nil)

func (s *DBStore) Load(provider string) (Token, error) {
	raw, err := s.DB.LoadSubscriptionCredential(context.Background(), provider)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, fmt.Errorf("%w: %s", ErrNoCredentials, provider)
	}
	if err != nil {
		return Token{}, err
	}
	var tok Token
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		// Decoder errors can contain input (including secrets).
		return Token{}, fmt.Errorf("invalid stored %s subscription credentials", provider)
	}
	return tok, nil
}

func (s *DBStore) Save(provider string, tok Token) error {
	raw, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("encode subscription credentials")
	}
	return s.DB.SaveSubscriptionCredential(context.Background(), provider, string(raw))
}

func (s *DBStore) Delete(provider string) error {
	return s.DB.DeleteSubscriptionCredential(context.Background(), provider)
}

func (s *DBStore) SaveRefreshed(provider string, previous, refreshed Token) error {
	before, err := json.Marshal(previous)
	if err != nil {
		return fmt.Errorf("encode previous subscription credentials")
	}
	after, err := json.Marshal(refreshed)
	if err != nil {
		return fmt.Errorf("encode refreshed subscription credentials")
	}
	changed, err := s.DB.RefreshSubscriptionCredential(context.Background(), provider, string(before), string(after))
	if err != nil {
		return err
	}
	if !changed {
		return ErrCredentialsChanged
	}
	return nil
}
