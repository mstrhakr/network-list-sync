package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mstrhakr/network-list-sync/internal/store"
)

func TestParseID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/42", nil)
	req.SetPathValue("id", "42")
	id, err := parseID(req)
	if err != nil {
		t.Fatalf("parseID() error = %v", err)
	}
	if id != 42 {
		t.Fatalf("parseID() = %d, want 42", id)
	}

	reqBad := httptest.NewRequest(http.MethodGet, "/api/jobs/x", nil)
	reqBad.SetPathValue("id", "x")
	if _, err := parseID(reqBad); err == nil {
		t.Fatal("expected parseID error for non-numeric ID")
	}
}

func TestWriteJSONAndWriteError(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusCreated, map[string]any{"ok": true})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusCreated)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("body = %q", rr.Body.String())
	}

	errRR := httptest.NewRecorder()
	writeError(errRR, http.StatusBadRequest, "bad")
	if errRR.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", errRR.Code, http.StatusBadRequest)
	}
	if !strings.Contains(errRR.Body.String(), `"error":"bad"`) {
		t.Fatalf("body = %q", errRR.Body.String())
	}
}

func TestStatusRecorderAndMiddleware(t *testing.T) {
	base := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: base}
	if _, err := rec.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if rec.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.status)
	}
	if rec.bytes != 5 {
		t.Fatalf("bytes = %d, want 5", rec.bytes)
	}
	if string(rec.preview) != "hello" {
		t.Fatalf("preview = %q", string(rec.preview))
	}

	h := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusBadRequest, "bad request")
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("middleware response status = %d", rr.Code)
	}

	htmlRR := httptest.NewRecorder()
	htmlRR.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !shouldLogResponsePreview("application/json") {
		t.Fatal("shouldLogResponsePreview(json) = false, want true")
	}
	if shouldLogResponsePreview(htmlRR.Header().Get("Content-Type")) {
		t.Fatal("shouldLogResponsePreview(html) = true, want false")
	}
}

func TestHealthEndpoint(t *testing.T) {
	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	h := &Handler{store: s}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.health(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("health status = %d", rr.Code)
	}
	var healthy map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &healthy); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if ok, _ := healthy["ok"].(bool); !ok {
		t.Fatalf("expected healthy response, got %s", rr.Body.String())
	}

	servers, err := s.ListDNSServers()
	if err != nil {
		t.Fatalf("ListDNSServers() error = %v", err)
	}
	for _, dns := range servers {
		dns.Enabled = false
		if err := s.UpdateDNSServer(&dns); err != nil {
			t.Fatalf("UpdateDNSServer(%d) error = %v", dns.ID, err)
		}
	}

	rr2 := httptest.NewRecorder()
	h.health(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Fatalf("health status (all disabled) = %d", rr2.Code)
	}
	var unhealthy map[string]any
	if err := json.Unmarshal(rr2.Body.Bytes(), &unhealthy); err != nil {
		t.Fatalf("decode unhealthy body: %v", err)
	}
	if ok, _ := unhealthy["ok"].(bool); ok {
		t.Fatalf("expected unhealthy response, got %s", rr2.Body.String())
	}
}

func TestGetRunTargetSnapshot(t *testing.T) {
	s, err := store.New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer s.Close()

	controllerID, err := s.CreateController(&store.Controller{Name: "test", URL: "http://example.test", APIKey: "key"})
	if err != nil {
		t.Fatalf("CreateController() error = %v", err)
	}
	jobID, err := s.CreateJob(&store.SyncJob{Name: "job", ControllerID: controllerID, NetworkListID: "list", Hostnames: "192.0.2.1"})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	logID, err := s.CreateRunLog(&store.RunLog{JobID: jobID, StartedAt: "2026-01-01T00:00:00Z", Status: "running"})
	if err != nil {
		t.Fatalf("CreateRunLog() error = %v", err)
	}
	finished := "2026-01-01T00:01:00Z"
	if err := s.UpdateRunLog(&store.RunLog{
		ID:         logID,
		FinishedAt: &finished,
		Status:     "success",
		Targets: []store.RunTargetSnapshot{{
			Label:      "unifi:list @ test",
			EntryCount: 1,
			Items:      json.RawMessage(`[{"type":"IP_ADDRESS","value":"192.0.2.1"}]`),
		}},
	}); err != nil {
		t.Fatalf("UpdateRunLog() error = %v", err)
	}
	logs, err := s.GetRunLogs(jobID, 10)
	if err != nil || len(logs) != 1 || len(logs[0].Targets) != 1 {
		t.Fatalf("GetRunLogs() = %+v, error = %v", logs, err)
	}

	h := &Handler{store: s}
	request := httptest.NewRequest(http.MethodGet, "/api/jobs/1/logs/1/targets/1", nil)
	request.SetPathValue("id", "1")
	request.SetPathValue("logID", "1")
	request.SetPathValue("targetID", "1")
	response := httptest.NewRecorder()
	h.getRunTargetSnapshot(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "192.0.2.1") {
		t.Fatalf("snapshot response = %d %s", response.Code, response.Body.String())
	}

	request.SetPathValue("id", "999")
	missingResponse := httptest.NewRecorder()
	h.getRunTargetSnapshot(missingResponse, request)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("wrong-job snapshot status = %d, want 404", missingResponse.Code)
	}
}
