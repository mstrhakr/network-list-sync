package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mstrhakr/network-list-sync/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	ProviderLocal      = "local"
	SessionCookieName  = "nls_session"
	DefaultSessionTTL  = 24 * time.Hour
	minimumPasswordLen = 12
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrAlreadyInitialized = errors.New("initial admin already exists")
	ErrSessionNotFound    = errors.New("session not found")
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
	ErrPasswordChangeNotAllowed = errors.New("password changes are only allowed for local users")
)

// Principal describes the authenticated app identity.
type Principal struct {
	UserID       int64
	Username     string
	AuthProvider string
	IsAdmin      bool
}

// PasswordAuthenticator verifies username/password credentials.
type PasswordAuthenticator interface {
	ProviderID() string
	Authenticate(ctx context.Context, username, password string) (*Principal, error)
}

// OIDCAuthenticator is a future extension point for OIDC-based login.
type OIDCAuthenticator interface {
	ProviderID() string
	BeginLogin(ctx context.Context, state, redirectURI string) (string, error)
	CompleteLogin(ctx context.Context, code, state, redirectURI string) (*Principal, error)
}

// Service coordinates auth providers and session issuance.
type Service struct {
	store             *store.Store
	passwordProviders map[string]PasswordAuthenticator
	oidcProviders     map[string]OIDCAuthenticator
	sessionTTL        time.Duration
	now               func() time.Time
}

func NewService(s *store.Store) *Service {
	svc := &Service{
		store:             s,
		passwordProviders: map[string]PasswordAuthenticator{},
		oidcProviders:     map[string]OIDCAuthenticator{},
		sessionTTL:        DefaultSessionTTL,
		now:               time.Now,
	}
	if s != nil {
		svc.RegisterPasswordProvider(&LocalPasswordAuthenticator{store: s})
	}
	return svc
}

func (s *Service) RegisterPasswordProvider(provider PasswordAuthenticator) {
	if provider == nil {
		return
	}
	s.passwordProviders[strings.ToLower(strings.TrimSpace(provider.ProviderID()))] = provider
}

func (s *Service) RegisterOIDCProvider(provider OIDCAuthenticator) {
	if provider == nil {
		return
	}
	s.oidcProviders[strings.ToLower(strings.TrimSpace(provider.ProviderID()))] = provider
}

func (s *Service) SupportsOIDC() bool {
	return len(s.oidcProviders) > 0
}

func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	if s.store == nil {
		return false, nil
	}
	count, err := s.store.CountUsers()
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Service) AuthenticatePassword(ctx context.Context, providerID, username, password string) (*Principal, error) {
	provider := s.passwordProviders[strings.ToLower(strings.TrimSpace(providerID))]
	if provider == nil {
		provider = s.passwordProviders[ProviderLocal]
	}
	if provider == nil {
		return nil, ErrInvalidCredentials
	}
	return provider.Authenticate(ctx, username, password)
}

func (s *Service) CreateInitialAdmin(ctx context.Context, username, password string) (*Principal, error) {
	if s.store == nil {
		return nil, fmt.Errorf("auth store not configured")
	}
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	count, err := s.store.CountUsers()
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrAlreadyInitialized
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	id, err := s.store.CreateUser(&store.AppUser{
		Username:     normalizeUsername(username),
		PasswordHash: hash,
		AuthProvider: ProviderLocal,
		IsAdmin:      true,
	})
	if err != nil {
		return nil, err
	}

	return &Principal{UserID: id, Username: normalizeUsername(username), AuthProvider: ProviderLocal, IsAdmin: true}, nil
}

func (s *Service) CreateSession(ctx context.Context, principal *Principal) (string, time.Time, error) {
	if s.store == nil || principal == nil {
		return "", time.Time{}, fmt.Errorf("auth session create failed")
	}
	if err := s.store.DeleteExpiredSessions(s.now().UTC().Format(time.RFC3339)); err != nil {
		return "", time.Time{}, err
	}
	rawToken, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := s.now().UTC().Add(s.sessionTTL)
	if err := s.store.CreateSession(principal.UserID, hashToken(rawToken), expiresAt.Format(time.RFC3339)); err != nil {
		return "", time.Time{}, err
	}
	return rawToken, expiresAt, nil
}

func (s *Service) PrincipalFromSessionToken(ctx context.Context, token string) (*Principal, error) {
	if s.store == nil {
		return nil, ErrSessionNotFound
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrSessionNotFound
	}
	nowRFC3339 := s.now().UTC().Format(time.RFC3339)
	user, err := s.store.GetUserBySessionTokenHash(hashToken(token), nowRFC3339)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	_ = s.store.TouchSession(hashToken(token), nowRFC3339)
	return &Principal{UserID: user.ID, Username: user.Username, AuthProvider: user.AuthProvider, IsAdmin: user.IsAdmin}, nil
}

func (s *Service) RevokeSession(ctx context.Context, token string) error {
	if s.store == nil {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return s.store.DeleteSessionByTokenHash(hashToken(token))
}

func (s *Service) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword, keepSessionToken string) error {
	_ = ctx
	if s.store == nil {
		return fmt.Errorf("auth store not configured")
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}

	u, err := s.store.GetUserByID(userID)
	if err != nil {
		return err
	}
	provider := strings.ToLower(strings.TrimSpace(u.AuthProvider))
	if provider != "" && provider != ProviderLocal {
		return ErrPasswordChangeNotAllowed
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(currentPassword)) != nil {
		return ErrInvalidCurrentPassword
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.UpdateUserPasswordHash(userID, hash); err != nil {
		return err
	}

	keepSessionToken = strings.TrimSpace(keepSessionToken)
	keepTokenHash := ""
	if keepSessionToken != "" {
		keepTokenHash = hashToken(keepSessionToken)
	}
	return s.store.DeleteSessionsByUserIDExceptTokenHash(userID, keepTokenHash)
}

func BootstrapInitialAdmin(s *store.Store, username, password string) (bool, error) {
	authSvc := NewService(s)
	if strings.TrimSpace(username) == "" && strings.TrimSpace(password) == "" {
		return false, nil
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return false, fmt.Errorf("both NLS_INITIAL_ADMIN_USERNAME and NLS_INITIAL_ADMIN_PASSWORD must be set")
	}
	_, err := authSvc.CreateInitialAdmin(context.Background(), username, password)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrAlreadyInitialized) {
		return false, nil
	}
	return false, err
}

// ValidatePassword enforces a baseline policy for local credentials.
func ValidatePassword(password string) error {
	if len(password) < minimumPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minimumPasswordLen)
	}
	return nil
}

func ValidateUsername(username string) error {
	u := normalizeUsername(username)
	if len(u) < 3 {
		return fmt.Errorf("username must be at least 3 characters")
	}
	if len(u) > 64 {
		return fmt.Errorf("username must be at most 64 characters")
	}
	return nil
}

func normalizeUsername(username string) string {
	return strings.TrimSpace(username)
}

func hashPassword(password string) (string, error) {
	encoded, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(encoded), nil
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
