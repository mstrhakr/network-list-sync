package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestStore_EndToEndCRUDAndObservedIPs(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Close()

	defaults, err := s.ListDNSServers()
	if err != nil {
		t.Fatalf("ListDNSServers() error = %v", err)
	}
	if len(defaults) == 0 {
		t.Fatalf("expected default DNS server seed")
	}

	ctrlID, err := s.CreateController(&Controller{
		Name:          "main",
		URL:           "https://unifi.local",
		APIKey:        "secret",
		SkipTLSVerify: true,
	})
	if err != nil {
		t.Fatalf("CreateController() error = %v", err)
	}

	ctrl, err := s.GetController(ctrlID)
	if err != nil {
		t.Fatalf("GetController() error = %v", err)
	}
	if ctrl.Site != "default" {
		t.Fatalf("controller site = %q, want default", ctrl.Site)
	}

	ctrl.Name = "main-updated"
	ctrl.Site = "site-a"
	if err := s.UpdateController(ctrl); err != nil {
		t.Fatalf("UpdateController() error = %v", err)
	}

	jobID, err := s.CreateJob(&SyncJob{
		Name:               "job-1",
		ControllerID:       ctrlID,
		NetworkListID:      "nl-1",
		Hostnames:          "example.com",
		Schedule:           "*/5 * * * *",
		ObservedIPTTLHours: -1,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}

	job, err := s.GetJob(jobID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.ObservedIPTTLHours != DefaultObservedIPTTLHours {
		t.Fatalf("ObservedIPTTLHours = %d, want %d", job.ObservedIPTTLHours, DefaultObservedIPTTLHours)
	}

	job.Schedule = "0 */6 * * *"
	job.Enabled = false
	job.ObservedIPTTLHours = 0
	if err := s.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob() error = %v", err)
	}

	jobs, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs() len = %d, want 1", len(jobs))
	}
	if jobs[0].ControllerName != "main-updated" {
		t.Fatalf("ControllerName = %q, want main-updated", jobs[0].ControllerName)
	}

	if err := s.UpdateJobLastRun(jobID, "2026-01-01T00:00:00Z", "success: done"); err != nil {
		t.Fatalf("UpdateJobLastRun() error = %v", err)
	}

	logID, err := s.CreateRunLog(&RunLog{JobID: jobID, StartedAt: "2026-01-01T00:00:00Z", Status: "running"})
	if err != nil {
		t.Fatalf("CreateRunLog() error = %v", err)
	}
	finished := "2026-01-01T00:01:00Z"
	if err := s.UpdateRunLog(&RunLog{ID: logID, FinishedAt: &finished, Status: "success", Message: "ok", ChangesMade: 2, Details: "d"}); err != nil {
		t.Fatalf("UpdateRunLog() error = %v", err)
	}
	logs, err := s.GetRunLogs(jobID, 10)
	if err != nil {
		t.Fatalf("GetRunLogs() error = %v", err)
	}
	if len(logs) != 1 || logs[0].Status != "success" {
		t.Fatalf("unexpected logs = %+v", logs)
	}

	dnsID, err := s.CreateDNSServer(&DNSServer{Name: "local", Address: "10.0.0.2:53", Enabled: false})
	if err != nil {
		t.Fatalf("CreateDNSServer() error = %v", err)
	}
	dnsEntry, err := s.GetDNSServer(dnsID)
	if err != nil {
		t.Fatalf("GetDNSServer() error = %v", err)
	}
	dnsEntry.Enabled = true
	dnsEntry.Address = "10.0.0.53:53"
	if err := s.UpdateDNSServer(dnsEntry); err != nil {
		t.Fatalf("UpdateDNSServer() error = %v", err)
	}
	enabledAddrs, err := s.ListEnabledDNSServerAddresses()
	if err != nil {
		t.Fatalf("ListEnabledDNSServerAddresses() error = %v", err)
	}
	if len(enabledAddrs) == 0 {
		t.Fatalf("expected at least one enabled DNS server address")
	}

	if err := s.UpsertObservedIPs(jobID, map[string]string{
		"203.0.113.10": "example.com",
		"203.0.113.11": "example.com",
	}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("UpsertObservedIPs() error = %v", err)
	}
	if err := s.UpsertObservedIPs(jobID, map[string]string{
		"203.0.113.10": "example.net",
	}, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("UpsertObservedIPs(update) error = %v", err)
	}
	observed, err := s.ListObservedIPs(jobID, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ListObservedIPs() error = %v", err)
	}
	if observed["203.0.113.10"] != "example.net" {
		t.Fatalf("observed source not updated: %+v", observed)
	}

	if err := s.DeleteExpiredObservedIPs(jobID, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("DeleteExpiredObservedIPs() error = %v", err)
	}
	observed, err = s.ListObservedIPs(jobID, "2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ListObservedIPs() post-delete error = %v", err)
	}
	if len(observed) != 1 {
		t.Fatalf("observed count = %d, want 1", len(observed))
	}

	if err := s.DeleteObservedIPs(jobID); err != nil {
		t.Fatalf("DeleteObservedIPs() error = %v", err)
	}
	observed, err = s.ListObservedIPs(jobID, "2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ListObservedIPs() post-clear error = %v", err)
	}
	if len(observed) != 0 {
		t.Fatalf("observed count after clear = %d, want 0", len(observed))
	}

	if err := s.DeleteDNSServer(dnsID); err != nil {
		t.Fatalf("DeleteDNSServer() error = %v", err)
	}
	if err := s.DeleteJob(jobID); err != nil {
		t.Fatalf("DeleteJob() error = %v", err)
	}
	if err := s.DeleteController(ctrlID); err != nil {
		t.Fatalf("DeleteController() error = %v", err)
	}
	if _, err := s.GetController(ctrlID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetController() after delete error = %v, want sql.ErrNoRows", err)
	}
}

func TestHelpers_NormalizeAndBool(t *testing.T) {
	if got := boolToInt(true); got != 1 {
		t.Fatalf("boolToInt(true) = %d, want 1", got)
	}
	if got := boolToInt(false); got != 0 {
		t.Fatalf("boolToInt(false) = %d, want 0", got)
	}
	if got := normalizeObservedIPTTLHours(-2); got != DefaultObservedIPTTLHours {
		t.Fatalf("normalizeObservedIPTTLHours(-2) = %d, want %d", got, DefaultObservedIPTTLHours)
	}
	if got := normalizeObservedIPTTLHours(0); got != 0 {
		t.Fatalf("normalizeObservedIPTTLHours(0) = %d, want 0", got)
	}
}
