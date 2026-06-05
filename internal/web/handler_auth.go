package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mstrhakr/network-list-sync/internal/auth"
	appLog "github.com/mstrhakr/network-list-sync/internal/logging"
)

type authContextKey string

const principalContextKey authContextKey = "principal"

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.auth == nil {
			next.ServeHTTP(w, r)
			return
		}

		if isPublicRoute(r) {
			if r.Method == http.MethodGet && r.URL.Path == "/login" {
				if principal, _ := h.sessionPrincipal(r); principal != nil {
					http.Redirect(w, r, "/", http.StatusSeeOther)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		principal, err := h.sessionPrincipal(r)
		if err != nil || principal == nil {
			h.clearSessionCookie(w)
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isPublicRoute(r *http.Request) bool {
	if r.URL.Path == "/api/health" {
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	if r.URL.Path == "/login" {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	if r.URL.Path == "/logo.png" {
		return r.Method == http.MethodGet
	}
	if strings.HasPrefix(r.URL.Path, "/static/") {
		return r.Method == http.MethodGet
	}
	return false
}

func (h *Handler) sessionPrincipal(r *http.Request) (*auth.Principal, error) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cookie.Value) == "" {
		return nil, auth.ErrSessionNotFound
	}
	return h.auth.PrincipalFromSessionToken(r.Context(), cookie.Value)
}

func principalFromContext(ctx context.Context) *auth.Principal {
	v := ctx.Value(principalContextKey)
	if p, ok := v.(*auth.Principal); ok {
		return p
	}
	return nil
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		http.Error(w, "auth is not configured", http.StatusServiceUnavailable)
		return
	}
	needsSetup, err := h.auth.NeedsSetup(r.Context())
	if err != nil {
		http.Error(w, "failed to determine setup state", http.StatusInternalServerError)
		return
	}
	h.renderLoginPage(w, loginViewData{NeedsSetup: needsSetup, ProviderID: auth.ProviderLocal}, http.StatusOK)
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		http.Error(w, "auth is not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderLoginPage(w, loginViewData{Error: "invalid form submission", ProviderID: auth.ProviderLocal}, http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	provider := strings.TrimSpace(r.FormValue("provider"))
	if provider == "" {
		provider = auth.ProviderLocal
	}

	needsSetup, err := h.auth.NeedsSetup(r.Context())
	if err != nil {
		h.renderLoginPage(w, loginViewData{Error: "failed to determine setup state", ProviderID: provider}, http.StatusInternalServerError)
		return
	}

	var principal *auth.Principal
	if needsSetup {
		confirm := r.FormValue("confirm_password")
		if password != confirm {
			h.renderLoginPage(w, loginViewData{NeedsSetup: true, Error: "password confirmation does not match", Username: username, ProviderID: provider}, http.StatusBadRequest)
			return
		}
		principal, err = h.auth.CreateInitialAdmin(r.Context(), username, password)
		if err != nil {
			h.renderLoginPage(w, loginViewData{NeedsSetup: true, Error: err.Error(), Username: username, ProviderID: provider}, http.StatusBadRequest)
			return
		}
	} else {
		principal, err = h.auth.AuthenticatePassword(r.Context(), provider, username, password)
		if err != nil {
			status := http.StatusUnauthorized
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				status = http.StatusBadRequest
			} else {
				appLog.GetManager().Warn("Failed login attempt",
					"username", username,
					"remote_addr", r.RemoteAddr,
					"path", r.URL.Path,
				)
			}
			h.renderLoginPage(w, loginViewData{Error: err.Error(), Username: username, ProviderID: provider}, status)
			return
		}
	}

	token, expiresAt, err := h.auth.CreateSession(r.Context(), principal)
	if err != nil {
		h.renderLoginPage(w, loginViewData{Error: "failed to create session", ProviderID: provider}, http.StatusInternalServerError)
		return
	}

	h.setSessionCookie(w, token, expiresAt)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if h.auth != nil {
		if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
			_ = h.auth.RevokeSession(r.Context(), cookie.Value)
		}
	}
	h.clearSessionCookie(w)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type loginViewData struct {
	NeedsSetup  bool
	Error       string
	Username    string
	ProviderID  string
	OIDCPlanned bool
}

func (h *Handler) renderLoginPage(w http.ResponseWriter, data loginViewData, status int) {
	if data.ProviderID == "" {
		data.ProviderID = auth.ProviderLocal
	}
	data.OIDCPlanned = true
	var buf bytes.Buffer
	if err := h.uiTmpl.ExecuteTemplate(&buf, "login", data); err != nil {
		http.Error(w, "failed to render login page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt.UTC(),
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
	})
}
