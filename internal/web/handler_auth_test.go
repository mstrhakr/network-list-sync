package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mstrhakr/network-list-sync/internal/store"
	"github.com/mstrhakr/network-list-sync/internal/syncer"
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

func TestAdminUserManagementAndUserPermissions(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	ctrlID, err := s.CreateController(&store.Controller{
		Name:          "main",
		URL:           "https://unifi.local",
		APIKey:        "secret",
		Site:          "default",
		SkipTLSVerify: true,
	})
	if err != nil {
		t.Fatalf("CreateController() error = %v", err)
	}
	jobID, err := s.CreateJob(&store.SyncJob{
		Name:          "job-1",
		ControllerID:  ctrlID,
		NetworkListID: "nl-1",
		Hostnames:     "example.com",
		Schedule:      "",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}

	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}<html><body>{{template "shell" .}}</body></html>{{end}}`)},
		"templates/login.gohtml":          {Data: []byte(`{{define "login"}}<html><body>{{if .Error}}ERR:{{.Error}}{{end}}{{if .NeedsSetup}}SETUP{{else}}LOGIN{{end}}</body></html>{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}hello{{end}}`)},
		"static/css/app.css":              {Data: []byte("body{}")},
		"logo.png":                        {Data: []byte("png")},
	}

	h := NewHandler(s, syncer.New(), nil, uiFS)

	adminSetup := url.Values{}
	adminSetup.Set("username", "admin")
	adminSetup.Set("password", "super-secure-pass")
	adminSetup.Set("confirm_password", "super-secure-pass")
	adminSetup.Set("provider", "local")

	setupReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(adminSetup.Encode()))
	setupReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setupRR := httptest.NewRecorder()
	h.ServeHTTP(setupRR, setupReq)
	if setupRR.Code != http.StatusSeeOther {
		t.Fatalf("setup POST /login status = %d, want %d", setupRR.Code, http.StatusSeeOther)
	}
	adminCookie := setupRR.Result().Cookies()[0]

	createUserPayload, _ := json.Marshal(map[string]any{
		"username": "operator",
		"password": "operator-secure-pass",
		"is_admin": false,
	})
	createUserReq := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(createUserPayload))
	createUserReq.Header.Set("Content-Type", "application/json")
	createUserReq.AddCookie(adminCookie)
	createUserRR := httptest.NewRecorder()
	h.ServeHTTP(createUserRR, createUserReq)
	if createUserRR.Code != http.StatusCreated {
		t.Fatalf("POST /api/users status = %d, want %d body=%s", createUserRR.Code, http.StatusCreated, createUserRR.Body.String())
	}

	userLogin := url.Values{}
	userLogin.Set("username", "operator")
	userLogin.Set("password", "operator-secure-pass")
	userLogin.Set("provider", "local")
	userLoginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(userLogin.Encode()))
	userLoginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	userLoginRR := httptest.NewRecorder()
	h.ServeHTTP(userLoginRR, userLoginReq)
	if userLoginRR.Code != http.StatusSeeOther {
		t.Fatalf("operator POST /login status = %d, want %d body=%s", userLoginRR.Code, http.StatusSeeOther, userLoginRR.Body.String())
	}
	userCookie := userLoginRR.Result().Cookies()[0]

	runReq := httptest.NewRequest(http.MethodPost, "/api/jobs/"+strconv.FormatInt(jobID, 10)+"/run", nil)
	runReq.AddCookie(userCookie)
	runRR := httptest.NewRecorder()
	h.ServeHTTP(runRR, runReq)
	if runRR.Code != http.StatusAccepted {
		t.Fatalf("POST /api/jobs/{id}/run status = %d, want %d body=%s", runRR.Code, http.StatusAccepted, runRR.Body.String())
	}

	createJobReq := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader([]byte(`{"name":"x","hostnames":"x"}`)))
	createJobReq.Header.Set("Content-Type", "application/json")
	createJobReq.AddCookie(userCookie)
	createJobRR := httptest.NewRecorder()
	h.ServeHTTP(createJobRR, createJobReq)
	if createJobRR.Code != http.StatusForbidden {
		t.Fatalf("POST /api/jobs as user status = %d, want %d body=%s", createJobRR.Code, http.StatusForbidden, createJobRR.Body.String())
	}

	instancesReq := httptest.NewRequest(http.MethodPost, "/api/instances", bytes.NewReader([]byte(`{"name":"x"}`)))
	instancesReq.Header.Set("Content-Type", "application/json")
	instancesReq.AddCookie(userCookie)
	instancesRR := httptest.NewRecorder()
	h.ServeHTTP(instancesRR, instancesReq)
	if instancesRR.Code != http.StatusForbidden {
		t.Fatalf("POST /api/instances as user status = %d, want %d body=%s", instancesRR.Code, http.StatusForbidden, instancesRR.Body.String())
	}

	listUsersReq := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	listUsersReq.AddCookie(userCookie)
	listUsersRR := httptest.NewRecorder()
	h.ServeHTTP(listUsersRR, listUsersReq)
	if listUsersRR.Code != http.StatusForbidden {
		t.Fatalf("GET /api/users as user status = %d, want %d body=%s", listUsersRR.Code, http.StatusForbidden, listUsersRR.Body.String())
	}
}
