package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mstrhakr/network-list-sync/internal/auth"
	"github.com/mstrhakr/network-list-sync/internal/scheduler"
	"github.com/mstrhakr/network-list-sync/internal/store"
	"github.com/mstrhakr/network-list-sync/internal/syncer"
)

type sourceListWebFixture struct {
	store  *store.Store
	h      http.Handler
	admin  *http.Cookie
	reader *http.Cookie
}

func newSourceListWebFixture(t *testing.T) *sourceListWebFixture {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	syn := syncer.New()
	sched := scheduler.New(s, syn)
	t.Cleanup(sched.Stop)
	uiFS := fstest.MapFS{
		"templates/index.gohtml":          {Data: []byte(`{{define "index"}}index{{end}}`)},
		"templates/login.gohtml":          {Data: []byte(`{{define "login"}}{{.Error}}{{end}}`)},
		"templates/partials/shell.gohtml": {Data: []byte(`{{define "shell"}}shell{{end}}`)},
		"static/css/app.css":              {Data: []byte("body{}")},
	}
	f := &sourceListWebFixture{store: s, h: NewHandler(s, syn, sched, uiFS)}
	f.admin = f.login(t, "admin", true)
	f.request(t, http.MethodPost, "/api/users", map[string]any{
		"username": "reader", "password": "source-lists-secure-pass", "is_admin": false,
	}, f.admin, http.StatusCreated)
	f.reader = f.login(t, "reader", false)
	return f
}

func (f *sourceListWebFixture) login(t *testing.T, username string, setup bool) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {username}, "password": {"source-lists-secure-pass"}, "provider": {"local"}}
	if setup {
		form.Set("confirm_password", "source-lists-secure-pass")
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther || len(rr.Result().Cookies()) == 0 {
		t.Fatalf("login %s: status=%d body=%s", username, rr.Code, rr.Body.String())
	}
	return rr.Result().Cookies()[0]
}

func (f *sourceListWebFixture) request(t *testing.T, method, path string, payload any, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if raw, ok := payload.(string); ok {
		body = []byte(raw)
	} else if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	if rr.Code != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, rr.Code, status, rr.Body.String())
	}
	if status >= 400 && !strings.Contains(rr.Body.String(), `"error":`) {
		t.Fatalf("%s %s: missing JSON error: %s", method, path, rr.Body.String())
	}
	return rr
}

func decodeSourceList(t *testing.T, rr *httptest.ResponseRecorder) store.SourceList {
	t.Helper()
	var list store.SourceList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func (f *sourceListWebFixture) createList(t *testing.T, name, hostnames string, ids ...int64) store.SourceList {
	t.Helper()
	return decodeSourceList(t, f.request(t, http.MethodPost, "/api/source-lists", store.SourceList{
		Name: name, Hostnames: hostnames, IncludedListIDs: ids,
	}, f.admin, http.StatusCreated))
}

func sourceListPath(id int64) string {
	return fmt.Sprintf("/api/source-lists/%d", id)
}

func TestSourceListHTTPCRUDAndNesting(t *testing.T) {
	f := newSourceListWebFixture(t)
	rr := f.request(t, http.MethodGet, "/api/source-lists", nil, f.reader, http.StatusOK)
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("empty lists = %s, want []", rr.Body.String())
	}
	leaf := f.createList(t, " leaf ", "192.0.2.1")
	if leaf.ID <= 0 || leaf.Name != "leaf" || leaf.CreatedAt == "" || leaf.UpdatedAt == "" {
		t.Fatalf("created list = %+v", leaf)
	}
	parent := f.createList(t, "parent", "", leaf.ID)
	root := f.createList(t, "root", "192.0.2.2", parent.ID)
	got := decodeSourceList(t, f.request(t, http.MethodGet, sourceListPath(root.ID), nil, f.reader, http.StatusOK))
	if got.ID != root.ID || !reflect.DeepEqual(got.IncludedListIDs, []int64{parent.ID}) {
		t.Fatalf("GET root = %+v", got)
	}
	rr = f.request(t, http.MethodGet, "/api/source-lists", nil, f.reader, http.StatusOK)
	var lists []store.SourceList
	if err := json.Unmarshal(rr.Body.Bytes(), &lists); err != nil || len(lists) != 3 {
		t.Fatalf("list response=%s error=%v", rr.Body.String(), err)
	}
	updated := decodeSourceList(t, f.request(t, http.MethodPut, sourceListPath(root.ID), map[string]any{
		"id": leaf.ID, "name": "renamed", "hostnames": "192.0.2.3", "included_list_ids": []int64{parent.ID},
	}, f.admin, http.StatusOK))
	if updated.ID != root.ID || updated.Name != "renamed" || updated.CreatedAt != root.CreatedAt {
		t.Fatalf("updated list = %+v", updated)
	}
	expanded, err := f.store.ExpandSourceEntries("", []int64{root.ID})
	if err != nil || !strings.Contains(expanded, "192.0.2.1") || !strings.Contains(expanded, "192.0.2.3") {
		t.Fatalf("nested expansion=%q error=%v", expanded, err)
	}
	f.request(t, http.MethodDelete, sourceListPath(leaf.ID), nil, f.admin, http.StatusConflict)
	f.request(t, http.MethodDelete, sourceListPath(root.ID), nil, f.admin, http.StatusNoContent)
	f.request(t, http.MethodGet, sourceListPath(root.ID), nil, f.reader, http.StatusNotFound)
	f.request(t, http.MethodDelete, sourceListPath(parent.ID), nil, f.admin, http.StatusNoContent)
	f.request(t, http.MethodDelete, sourceListPath(leaf.ID), nil, f.admin, http.StatusNoContent)
}

func TestSourceListHTTPAuthentication(t *testing.T) {
	f := newSourceListWebFixture(t)
	list := f.createList(t, "list", "192.0.2.1")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/source-lists"},
		{http.MethodGet, sourceListPath(list.ID)},
		{http.MethodPost, "/api/source-lists"},
		{http.MethodPut, sourceListPath(list.ID)},
		{http.MethodDelete, sourceListPath(list.ID)},
	} {
		t.Run("unauthenticated_"+tc.method+tc.path, func(t *testing.T) {
			f.request(t, tc.method, tc.path, nil, nil, http.StatusUnauthorized)
		})
		if tc.method != http.MethodGet {
			t.Run("reader_"+tc.method, func(t *testing.T) {
				// Authorization must run before JSON decoding or any mutation.
				f.request(t, tc.method, tc.path, "{", f.reader, http.StatusForbidden)
			})
		}
	}
	f.request(t, http.MethodPost, "/api/resolve", `{"included_list_ids":[1]}`, f.reader, http.StatusForbidden)
	f.request(t, http.MethodPost, "/api/jobs", `{"included_list_ids":[1]}`, f.reader, http.StatusForbidden)
	f.request(t, http.MethodPut, "/api/jobs/1", `{"included_list_ids":[1]}`, f.reader, http.StatusForbidden)
	got, err := f.store.GetSourceList(list.ID)
	if err != nil || got.Name != "list" {
		t.Fatalf("unauthorized mutation: list=%+v err=%v", got, err)
	}
}

func TestSourceListHTTPValidationAndMissing(t *testing.T) {
	f := newSourceListWebFixture(t)
	leaf := f.createList(t, "leaf", "192.0.2.1")
	parent := f.createList(t, "parent", "", leaf.ID)
	for _, tc := range []struct {
		name, method, path string
		payload            any
		status             int
	}{
		{"create JSON", http.MethodPost, "/api/source-lists", "{", http.StatusBadRequest},
		{"create name", http.MethodPost, "/api/source-lists", `{"name":" "}`, http.StatusBadRequest},
		{"create empty content", http.MethodPost, "/api/source-lists", `{"name":"empty","hostnames":" "}`, http.StatusBadRequest},
		{"create reference", http.MethodPost, "/api/source-lists", `{"name":"invalid","included_list_ids":[99999]}`, http.StatusBadRequest},
		{"create negative reference", http.MethodPost, "/api/source-lists", `{"name":"invalid","included_list_ids":[-1]}`, http.StatusBadRequest},
		{"update JSON", http.MethodPut, sourceListPath(leaf.ID), "{", http.StatusBadRequest},
		{"update name", http.MethodPut, sourceListPath(leaf.ID), `{"name":" "}`, http.StatusBadRequest},
		{"update empty content", http.MethodPut, sourceListPath(leaf.ID), `{"name":"empty"}`, http.StatusBadRequest},
		{"update reference", http.MethodPut, sourceListPath(leaf.ID), `{"name":"invalid","included_list_ids":[99999]}`, http.StatusBadRequest},
		{"self cycle", http.MethodPut, sourceListPath(leaf.ID), store.SourceList{Name: "cycle", IncludedListIDs: []int64{leaf.ID}}, http.StatusBadRequest},
		{"nested cycle", http.MethodPut, sourceListPath(leaf.ID), store.SourceList{Name: "cycle", IncludedListIDs: []int64{parent.ID}}, http.StatusBadRequest},
		{"missing get", http.MethodGet, sourceListPath(99999), nil, http.StatusNotFound},
		{"missing update", http.MethodPut, sourceListPath(99999), store.SourceList{Name: "missing", Hostnames: "192.0.2.1"}, http.StatusNotFound},
		{"missing delete", http.MethodDelete, sourceListPath(99999), nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.request(t, tc.method, tc.path, tc.payload, f.admin, tc.status)
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, id := range []string{"x", "0", "-1"} {
			f.request(t, method, "/api/source-lists/"+id, `{"name":"valid"}`, f.admin, http.StatusBadRequest)
		}
	}
	got, err := f.store.GetSourceList(leaf.ID)
	if err != nil || got.Name != "leaf" || got.Hostnames != "192.0.2.1" || len(got.IncludedListIDs) != 0 {
		t.Fatalf("invalid updates changed leaf: %+v err=%v", got, err)
	}
}

func TestSourceListHTTPPreview(t *testing.T) {
	f := newSourceListWebFixture(t)
	leaf := f.createList(t, "leaf", "192.0.2.1")
	parent := f.createList(t, "parent", "192.0.2.2", leaf.ID)
	rr := f.request(t, http.MethodPost, "/api/resolve", map[string]any{
		"hostnames": "192.0.2.3", "included_list_ids": []int64{parent.ID},
	}, f.admin, http.StatusOK)
	var result []struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 || result[0].IP != "192.0.2.1" || result[1].IP != "192.0.2.2" || result[2].IP != "192.0.2.3" {
		t.Fatalf("nested preview=%s", rr.Body.String())
	}
	rr = f.request(t, http.MethodPost, "/api/resolve", map[string]any{"included_list_ids": []int64{leaf.ID}}, f.admin, http.StatusOK)
	if !strings.Contains(rr.Body.String(), "192.0.2.1") {
		t.Fatalf("includes-only preview=%s", rr.Body.String())
	}
	// Graph validation precedes DNS configuration checks.
	servers, err := f.store.ListDNSServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		server.Enabled = false
		if err := f.store.UpdateDNSServer(&server); err != nil {
			t.Fatal(err)
		}
	}
	for _, payload := range []string{`{"included_list_ids":[99999]}`, `{"hostnames":"192.0.2.4","included_list_ids":[-1]}`, "{"} {
		f.request(t, http.MethodPost, "/api/resolve", payload, f.admin, http.StatusBadRequest)
	}
	f.request(t, http.MethodPost, "/api/resolve", map[string]any{"included_list_ids": []int64{leaf.ID}}, f.admin, http.StatusUnprocessableEntity)
}

func TestSourceListHTTPIncludesOnlyJobs(t *testing.T) {
	f := newSourceListWebFixture(t)
	leaf := f.createList(t, "leaf", "192.0.2.1")
	parent := f.createList(t, "parent", "", leaf.ID)
	controllerID, err := f.store.CreateController(&store.Controller{Name: "test", URL: "https://example.test", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	job := store.SyncJob{Name: "includes-only", ControllerID: controllerID, NetworkListID: "target", IncludedListIDs: []int64{parent.ID}}
	rr := f.request(t, http.MethodPost, "/api/jobs", job, f.admin, http.StatusCreated)
	var saved store.SyncJob
	if err := json.Unmarshal(rr.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/jobs/%d", saved.ID)
	got, err := f.store.GetJob(saved.ID)
	if err != nil || got.Hostnames != "" || !reflect.DeepEqual(got.IncludedListIDs, job.IncludedListIDs) {
		t.Fatalf("saved job=%+v err=%v", got, err)
	}
	f.request(t, http.MethodDelete, sourceListPath(parent.ID), nil, f.admin, http.StatusConflict)
	job.IncludedListIDs = []int64{leaf.ID}
	f.request(t, http.MethodPut, path, job, f.admin, http.StatusOK)
	f.request(t, http.MethodGet, path, nil, f.reader, http.StatusOK)
	f.request(t, http.MethodGet, "/api/jobs", nil, f.reader, http.StatusOK)
	for _, ids := range [][]int64{{99999}, {-1}, nil} {
		job.IncludedListIDs = ids
		f.request(t, http.MethodPost, "/api/jobs", job, f.admin, http.StatusBadRequest)
		f.request(t, http.MethodPut, path, job, f.admin, http.StatusBadRequest)
	}
	got, err = f.store.GetJob(saved.ID)
	if err != nil || !reflect.DeepEqual(got.IncludedListIDs, []int64{leaf.ID}) {
		t.Fatalf("invalid update changed job=%+v err=%v", got, err)
	}
	// Existing inline-only jobs remain valid, and omitting includes clears them.
	job.Hostnames = "192.0.2.2"
	job.IncludedListIDs = nil
	f.request(t, http.MethodPut, path, job, f.admin, http.StatusOK)
	got, err = f.store.GetJob(saved.ID)
	if err != nil || got.Hostnames != job.Hostnames || len(got.IncludedListIDs) != 0 {
		t.Fatalf("inline-only update=%+v err=%v", got, err)
	}
	job.Hostnames = ""
	job.IncludedListIDs = []int64{leaf.ID}
	f.request(t, http.MethodPut, path, job, f.admin, http.StatusOK)
	f.request(t, http.MethodDelete, sourceListPath(leaf.ID), nil, f.admin, http.StatusConflict)
	f.request(t, http.MethodDelete, path, nil, f.admin, http.StatusNoContent)
	f.request(t, http.MethodDelete, sourceListPath(parent.ID), nil, f.admin, http.StatusNoContent)
	f.request(t, http.MethodDelete, sourceListPath(leaf.ID), nil, f.admin, http.StatusNoContent)
}

func TestSourceListJobsWithoutScheduler(t *testing.T) {
	f := newSourceListWebFixture(t)
	list := f.createList(t, "list", "192.0.2.1")
	controllerID, err := f.store.CreateController(&store.Controller{Name: "test", URL: "https://example.test", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: f.store}
	job := store.SyncJob{Name: "job", ControllerID: controllerID, NetworkListID: "target", IncludedListIDs: []int64{list.ID}}
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	request := func(fn http.HandlerFunc, id string, status int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(body))
		req.SetPathValue("id", id)
		req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &auth.Principal{IsAdmin: true}))
		rr := httptest.NewRecorder()
		fn(rr, req)
		if rr.Code != status {
			t.Fatalf("status=%d want=%d body=%s", rr.Code, status, rr.Body.String())
		}
		return rr
	}
	rr := request(h.createJob, "", http.StatusCreated)
	if err := json.Unmarshal(rr.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(job.ID)
	request(h.updateJob, id, http.StatusOK)
	request(h.deleteJob, id, http.StatusNoContent)
}

func TestSourceListHTTPDatabaseErrors(t *testing.T) {
	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: s}
	for _, fn := range []http.HandlerFunc{h.listSourceLists, h.getSourceList, h.createSourceList, h.updateSourceList, h.deleteSourceList} {
		req := httptest.NewRequest(http.MethodPost, "/api/source-lists/1", strings.NewReader(`{"name":"list","hostnames":"192.0.2.1"}`))
		req.SetPathValue("id", "1")
		req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &auth.Principal{IsAdmin: true}))
		rr := httptest.NewRecorder()
		fn(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("closed DB status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
}

func TestJobSourceHTTPDatabaseErrors(t *testing.T) {
	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: s}
	for _, tc := range []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"create", h.createJob},
		{"update", h.updateJob},
		{"preview", h.resolveHostnames},
	} {
		for _, entries := range []string{`"hostnames":"192.0.2.1"`, `"included_list_ids":[1]`} {
			t.Run(tc.name+"/"+entries, func(t *testing.T) {
				body := `{"name":"job","instance_id":1,"target_list_id":"target",` + entries + `}`
				req := httptest.NewRequest(http.MethodPost, "/api/jobs/1", strings.NewReader(body))
				req.SetPathValue("id", "1")
				req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &auth.Principal{IsAdmin: true}))
				rr := httptest.NewRecorder()
				tc.fn(rr, req)
				if rr.Code != http.StatusInternalServerError {
					t.Fatalf("closed DB status=%d want=500 body=%s", rr.Code, rr.Body.String())
				}
			})
		}
	}
}

func TestSourceErrorClassificationUsesType(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"wrapped validation", fmt.Errorf("nested source: %w", &store.SourceValidationError{Message: "invalid reference"}), http.StatusBadRequest},
		{"storage", errors.New("database is closed"), http.StatusInternalServerError},
		{"validation-like storage text", errors.New("source list ID 1 does not exist"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, write := range []func(http.ResponseWriter, error){writeSourceEntriesError, writeSourceListError} {
				rr := httptest.NewRecorder()
				write(rr, tc.err)
				if rr.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.status, rr.Body.String())
				}
			}
		})
	}
}
