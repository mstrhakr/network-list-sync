package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/mstrhakr/network-list-sync/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// LocalPasswordAuthenticator validates credentials against app_users.
type LocalPasswordAuthenticator struct {
	store *store.Store
}

func (a *LocalPasswordAuthenticator) ProviderID() string {
	return ProviderLocal
}

func (a *LocalPasswordAuthenticator) Authenticate(ctx context.Context, username, password string) (*Principal, error) {
	_ = ctx
	if a.store == nil {
		return nil, ErrInvalidCredentials
	}
	u, err := a.store.GetUserByUsername(normalizeUsername(username))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if strings.TrimSpace(u.AuthProvider) != "" && strings.ToLower(strings.TrimSpace(u.AuthProvider)) != ProviderLocal {
		return nil, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	return &Principal{UserID: u.ID, Username: u.Username, AuthProvider: ProviderLocal, IsAdmin: u.IsAdmin}, nil
}
