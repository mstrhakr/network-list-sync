package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mstrhakr/network-list-sync/internal/store"
)

func TestAuthRedirectThenSetupLogin(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}<html><body>{{template "shell" .}}</body></html>{{end}}`)},
		"templates/login.gohtml":          {Data: []byte(`{{define "login"}}<html><body>{{if .Error}}ERR:{{.Error}}{{end}}{{if .NeedsSetup}}SETUP{{else}}LOGIN{{end}}</body></html>{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}hello{{end}}`)},
		"static/css/app.css":              {Data: []byte("body{}")},
		"logo.png":                        {Data: []byte("png")},
	}

	h := NewHandler(s, nil, nil, uiFS)

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRR := httptest.NewRecorder()
	h.ServeHTTP(rootRR, rootReq)
	if rootRR.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want %d", rootRR.Code, http.StatusSeeOther)
	}
	if loc := rootRR.Header().Get("Location"); loc != "/login" {
		t.Fatalf("GET / redirect = %q, want /login", loc)
	}

	loginReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	loginRR := httptest.NewRecorder()
	h.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d, want 200", loginRR.Code)
	}
	if !strings.Contains(loginRR.Body.String(), "SETUP") {
		t.Fatalf("GET /login body = %q, want setup mode", loginRR.Body.String())
	}

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "super-secure-pass")
	form.Set("confirm_password", "super-secure-pass")
	form.Set("provider", "local")

	postReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRR := httptest.NewRecorder()
	h.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusSeeOther {
		t.Fatalf("POST /login status = %d, want %d", postRR.Code, http.StatusSeeOther)
	}
	if loc := postRR.Header().Get("Location"); loc != "/" {
		t.Fatalf("POST /login redirect = %q, want /", loc)
	}
	cookie := postRR.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatalf("POST /login set-cookie missing")
	}

	authedReq := httptest.NewRequest(http.MethodGet, "/", nil)
	authedReq.AddCookie(cookie[0])
	authedRR := httptest.NewRecorder()
	h.ServeHTTP(authedRR, authedReq)
	if authedRR.Code != http.StatusOK {
		t.Fatalf("GET / (authed) status = %d, want 200", authedRR.Code)
	}
	if !strings.Contains(authedRR.Body.String(), "hello") {
		t.Fatalf("GET / (authed) body = %q, want rendered shell", authedRR.Body.String())
	}
}

func TestHealthIsPublicWithoutAuth(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}<html><body>{{template "shell" .}}</body></html>{{end}}`)},
		"templates/login.gohtml":          {Data: []byte(`{{define "login"}}<html><body>login</body></html>{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}hello{{end}}`)},
		"static/css/app.css":              {Data: []byte("body{}")},
		"logo.png":                        {Data: []byte("png")},
	}

	h := NewHandler(s, nil, nil, uiFS)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/health status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestChangePasswordAPI(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}<html><body>{{template "shell" .}}</body></html>{{end}}`)},
		"templates/login.gohtml":          {Data: []byte(`{{define "login"}}<html><body>{{if .Error}}ERR:{{.Error}}{{end}}{{if .NeedsSetup}}SETUP{{else}}LOGIN{{end}}</body></html>{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}hello{{end}}`)},
		"static/css/app.css":              {Data: []byte("body{}")},
		"logo.png":                        {Data: []byte("png")},
	}

	h := NewHandler(s, nil, nil, uiFS)

	setupForm := url.Values{}
	setupForm.Set("username", "admin")
	setupForm.Set("password", "super-secure-pass")
	setupForm.Set("confirm_password", "super-secure-pass")
	setupForm.Set("provider", "local")

	setupReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(setupForm.Encode()))
	setupReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setupRR := httptest.NewRecorder()
	h.ServeHTTP(setupRR, setupReq)
	if setupRR.Code != http.StatusSeeOther {
		t.Fatalf("setup POST /login status = %d, want %d", setupRR.Code, http.StatusSeeOther)
	}
	cookies := setupRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("setup POST /login set-cookie missing")
	}

	payload, _ := json.Marshal(map[string]string{
		"current_password": "super-secure-pass",
		"new_password":     "super-duper-secure-pass",
		"confirm_password": "super-duper-secure-pass",
	})
	changeReq := httptest.NewRequest(http.MethodPost, "/api/account/password", bytes.NewReader(payload))
	changeReq.Header.Set("Content-Type", "application/json")
	changeReq.AddCookie(cookies[0])
	changeRR := httptest.NewRecorder()
	h.ServeHTTP(changeRR, changeReq)
	if changeRR.Code != http.StatusOK {
		t.Fatalf("POST /api/account/password status = %d, want %d body=%s", changeRR.Code, http.StatusOK, changeRR.Body.String())
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(cookies[0])
	logoutRR := httptest.NewRecorder()
	h.ServeHTTP(logoutRR, logoutReq)

	oldLogin := url.Values{}
	oldLogin.Set("username", "admin")
	oldLogin.Set("password", "super-secure-pass")
	oldLogin.Set("provider", "local")
	oldReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(oldLogin.Encode()))
	oldReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	oldRR := httptest.NewRecorder()
	h.ServeHTTP(oldRR, oldReq)
	if oldRR.Code != http.StatusUnauthorized {
		t.Fatalf("POST /login old password status = %d, want %d", oldRR.Code, http.StatusUnauthorized)
	}

	newLogin := url.Values{}
	newLogin.Set("username", "admin")
	newLogin.Set("password", "super-duper-secure-pass")
	newLogin.Set("provider", "local")
	newReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(newLogin.Encode()))
	newReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	newRR := httptest.NewRecorder()
	h.ServeHTTP(newRR, newReq)
	if newRR.Code != http.StatusSeeOther {
		t.Fatalf("POST /login new password status = %d, want %d", newRR.Code, http.StatusSeeOther)
	}
}
