package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mstrhakr/network-list-sync/internal/store"
)

func TestBootstrapInitialAdmin(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	created, err := BootstrapInitialAdmin(s, "Admin", "super-secure-pass")
	if err != nil {
		t.Fatalf("BootstrapInitialAdmin() error = %v", err)
	}
	if !created {
		t.Fatalf("BootstrapInitialAdmin() created = false, want true")
	}

	createdAgain, err := BootstrapInitialAdmin(s, "Admin", "super-secure-pass")
	if err != nil {
		t.Fatalf("BootstrapInitialAdmin(second) error = %v", err)
	}
	if createdAgain {
		t.Fatalf("BootstrapInitialAdmin(second) created = true, want false")
	}
}

func TestLocalAuthAndSession(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	svc := NewService(s)
	principal, err := svc.CreateInitialAdmin(context.Background(), "admin", "super-secure-pass")
	if err != nil {
		t.Fatalf("CreateInitialAdmin() error = %v", err)
	}
	if !principal.IsAdmin {
		t.Fatalf("principal.IsAdmin = false, want true")
	}

	authed, err := svc.AuthenticatePassword(context.Background(), ProviderLocal, "admin", "super-secure-pass")
	if err != nil {
		t.Fatalf("AuthenticatePassword() error = %v", err)
	}
	if authed.UserID == 0 {
		t.Fatalf("AuthenticatePassword() user id = 0")
	}

	token, _, err := svc.CreateSession(context.Background(), authed)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if token == "" {
		t.Fatalf("CreateSession() token is empty")
	}

	fromSession, err := svc.PrincipalFromSessionToken(context.Background(), token)
	if err != nil {
		t.Fatalf("PrincipalFromSessionToken() error = %v", err)
	}
	if fromSession.Username != "admin" {
		t.Fatalf("session username = %q, want admin", fromSession.Username)
	}

	if err := svc.RevokeSession(context.Background(), token); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := svc.PrincipalFromSessionToken(context.Background(), token); err == nil {
		t.Fatalf("PrincipalFromSessionToken() after revoke: expected error")
	}
}

func TestUsernamesPreserveCaseAndAuthenticateExactly(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	svc := NewService(s)
	principal, err := svc.CreateInitialAdmin(context.Background(), "AdminUser", "super-secure-pass")
	if err != nil {
		t.Fatalf("CreateInitialAdmin() error = %v", err)
	}
	if principal.Username != "AdminUser" {
		t.Fatalf("principal.Username = %q, want AdminUser", principal.Username)
	}

	authed, err := svc.AuthenticatePassword(context.Background(), ProviderLocal, "AdminUser", "super-secure-pass")
	if err != nil {
		t.Fatalf("AuthenticatePassword(exact case) error = %v", err)
	}
	if authed.Username != "AdminUser" {
		t.Fatalf("authed.Username = %q, want AdminUser", authed.Username)
	}

	if _, err := svc.AuthenticatePassword(context.Background(), ProviderLocal, "adminuser", "super-secure-pass"); err == nil {
		t.Fatalf("AuthenticatePassword(lowercase) error = nil, want invalid credentials")
	}
}

func TestChangePasswordKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	svc := NewService(s)
	principal, err := svc.CreateInitialAdmin(context.Background(), "admin", "super-secure-pass")
	if err != nil {
		t.Fatalf("CreateInitialAdmin() error = %v", err)
	}

	tokenKeep, _, err := svc.CreateSession(context.Background(), principal)
	if err != nil {
		t.Fatalf("CreateSession(keep) error = %v", err)
	}
	tokenRevoke, _, err := svc.CreateSession(context.Background(), principal)
	if err != nil {
		t.Fatalf("CreateSession(revoke) error = %v", err)
	}

	if err := svc.ChangePassword(context.Background(), principal.UserID, "super-secure-pass", "even-more-secure-pass", tokenKeep); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}

	if _, err := svc.AuthenticatePassword(context.Background(), ProviderLocal, "admin", "super-secure-pass"); err == nil {
		t.Fatalf("AuthenticatePassword(old password) error = nil, want invalid credentials")
	}
	if _, err := svc.AuthenticatePassword(context.Background(), ProviderLocal, "admin", "even-more-secure-pass"); err != nil {
		t.Fatalf("AuthenticatePassword(new password) error = %v", err)
	}

	if _, err := svc.PrincipalFromSessionToken(context.Background(), tokenKeep); err != nil {
		t.Fatalf("PrincipalFromSessionToken(keep) error = %v", err)
	}
	if _, err := svc.PrincipalFromSessionToken(context.Background(), tokenRevoke); err == nil {
		t.Fatalf("PrincipalFromSessionToken(revoked) error = nil, want error")
	}

	if err := svc.ChangePassword(context.Background(), principal.UserID, "wrong-current", "super-another-pass", tokenKeep); err == nil {
		t.Fatalf("ChangePassword(wrong current) error = nil, want error")
	}
}

func TestManageUsersAndAdminSafeguards(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	svc := NewService(s)
	admin, err := svc.CreateInitialAdmin(context.Background(), "admin", "super-secure-pass")
	if err != nil {
		t.Fatalf("CreateInitialAdmin() error = %v", err)
	}

	user, err := svc.CreateUser(context.Background(), "operator", "operator-secure-pass", false)
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if user.ID == 0 {
		t.Fatalf("CreateUser() returned invalid ID")
	}

	if _, err := svc.CreateUser(context.Background(), "operator", "another-secure-pass", false); err == nil {
		t.Fatalf("CreateUser(duplicate username) error = nil, want error")
	}

	updated, err := svc.UpdateUser(context.Background(), user.ID, "operator-renamed", true, "")
	if err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	if !updated.IsAdmin {
		t.Fatalf("UpdateUser() IsAdmin = false, want true")
	}

	if _, err := svc.UpdateUser(context.Background(), admin.UserID, "admin", false, ""); err != nil {
		t.Fatalf("UpdateUser(demote sole admin with second admin present) error = %v", err)
	}

	if err := svc.DeleteUser(context.Background(), admin.UserID, updated.ID); err == nil {
		t.Fatalf("DeleteUser(last admin) error = nil, want error")
	}

	if _, err := svc.UpdateUser(context.Background(), admin.UserID, "admin", true, ""); err != nil {
		t.Fatalf("UpdateUser(re-promote admin) error = %v", err)
	}

	if err := svc.DeleteUser(context.Background(), admin.UserID, admin.UserID); err == nil {
		t.Fatalf("DeleteUser(self) error = nil, want error")
	}

	if err := svc.DeleteUser(context.Background(), admin.UserID, updated.ID); err != nil {
		t.Fatalf("DeleteUser(operator) error = %v", err)
	}

	if _, err := svc.UpdateUser(context.Background(), admin.UserID, "admin", false, ""); err == nil {
		t.Fatalf("UpdateUser(demote last admin) error = nil, want error")
	}
}
