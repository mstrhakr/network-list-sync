package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const DefaultObservedIPTTLHours = 7 * 24

// Controller represents a saved provider instance configuration.
type Controller struct {
	ID            int64  `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	APIKey        string `json:"api_key,omitempty"`
	Site          string `json:"site"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// SyncJob represents a configured sync job.
type SyncJob struct {
	ID                 int64       `json:"id"`
	Name               string      `json:"name"`
	ControllerID       int64       `json:"instance_id"`
	NetworkListID      string      `json:"target_list_id"`
	Targets            []JobTarget `json:"targets,omitempty"`
	Hostnames          string      `json:"hostnames"`
	Schedule           string      `json:"schedule"`
	ObservedIPTTLHours int         `json:"observed_ip_ttl_hours"`
	Enabled            bool        `json:"enabled"`
	LastRunAt          *string     `json:"last_run_at"`
	LastResult         *string     `json:"last_result"`
	CreatedAt          string      `json:"created_at"`
	UpdatedAt          string      `json:"updated_at"`
	ControllerName     string      `json:"instance_name,omitempty"`
}

// JobTarget binds one sync job to one endpoint/list pair.
type JobTarget struct {
	ID             int64  `json:"id"`
	JobID          int64  `json:"job_id"`
	ControllerID   int64  `json:"instance_id"`
	NetworkListID  string `json:"target_list_id"`
	ControllerName string `json:"instance_name,omitempty"`
	Provider       string `json:"provider,omitempty"`
}

// RunLog represents a single execution record for a sync job.
type RunLog struct {
	ID          int64   `json:"id"`
	JobID       int64   `json:"job_id"`
	StartedAt   string  `json:"started_at"`
	FinishedAt  *string `json:"finished_at"`
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	ChangesMade int     `json:"changes_made"`
	Details     string  `json:"details"`
}

// DNSServer represents a custom DNS resolver endpoint.
type DNSServer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Store provides SQLite-backed persistence for sync jobs and run logs.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database and runs migrations.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS controllers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'unifi',
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			api_key TEXT NOT NULL,
			site TEXT NOT NULL DEFAULT 'default',
			skip_tls_verify INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS sync_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			controller_id INTEGER NOT NULL,
			network_list_id TEXT NOT NULL,
			hostnames TEXT NOT NULL,
			schedule TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run_at TEXT,
			last_result TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (controller_id) REFERENCES controllers(id)
		);
		CREATE TABLE IF NOT EXISTS run_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id INTEGER NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			status TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			changes_made INTEGER NOT NULL DEFAULT 0,
			details TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (job_id) REFERENCES sync_jobs(id) ON DELETE CASCADE
		);
	`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE sync_jobs ADD COLUMN observed_ip_ttl_hours INTEGER NOT NULL DEFAULT 168`)
	_, _ = s.db.Exec(`ALTER TABLE controllers ADD COLUMN provider TEXT NOT NULL DEFAULT 'unifi'`)
	// Add skip_tls_verify to existing databases that predate this column.
	_, _ = s.db.Exec(`ALTER TABLE controllers ADD COLUMN skip_tls_verify INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS sync_job_targets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id INTEGER NOT NULL,
		controller_id INTEGER NOT NULL,
		network_list_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(job_id, controller_id, network_list_id),
		FOREIGN KEY (job_id) REFERENCES sync_jobs(id) ON DELETE CASCADE,
		FOREIGN KEY (controller_id) REFERENCES controllers(id) ON DELETE CASCADE
	)`)
	// Add dns_servers table for resolver endpoints.
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS dns_servers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		address TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	// Cache recently observed DNS answers per job so short-lived rotations do not cause firewall flapping.
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS job_observed_ips (
		job_id INTEGER NOT NULL,
		ip TEXT NOT NULL,
		source TEXT NOT NULL,
		first_seen_at TEXT NOT NULL,
		last_seen_at TEXT NOT NULL,
		PRIMARY KEY (job_id, ip),
		FOREIGN KEY (job_id) REFERENCES sync_jobs(id) ON DELETE CASCADE
	)`)
	// Seed the well-known public resolvers on first run.
	var dnsCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dns_servers`).Scan(&dnsCount); err == nil && dnsCount == 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		defaults := []struct{ name, address string }{
			{"Cloudflare (1.1.1.1)", "1.1.1.1:53"},
			{"Cloudflare (1.0.0.1)", "1.0.0.1:53"},
			{"Google (8.8.8.8)", "8.8.8.8:53"},
			{"Google (8.8.4.4)", "8.8.4.4:53"},
			{"Quad9 (9.9.9.9)", "9.9.9.9:53"},
			{"Quad9 (149.112.112.112)", "149.112.112.112:53"},
			{"OpenDNS (208.67.222.222)", "208.67.222.222:53"},
			{"OpenDNS (208.67.220.220)", "208.67.220.220:53"},
		}
		for _, d := range defaults {
			_, _ = s.db.Exec(
				`INSERT INTO dns_servers (name, address, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
				d.name, d.address, now, now)
		}
	}

	// Backfill one target per existing job for databases created before sync_job_targets.
	rows, err := s.db.Query(`SELECT id, controller_id, network_list_id FROM sync_jobs`)
	if err == nil {
		defer rows.Close()
		now := time.Now().UTC().Format(time.RFC3339)
		for rows.Next() {
			var jobID int64
			var controllerID int64
			var networkListID string
			if scanErr := rows.Scan(&jobID, &controllerID, &networkListID); scanErr != nil {
				continue
			}
			if controllerID == 0 || networkListID == "" {
				continue
			}
			_, _ = s.db.Exec(`
				INSERT INTO sync_job_targets (job_id, controller_id, network_list_id, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(job_id, controller_id, network_list_id) DO NOTHING`,
				jobID, controllerID, networkListID, now, now)
		}
	}

	return nil
}

// ---------- Controller CRUD ----------

func (s *Store) ListControllers() ([]Controller, error) {
	rows, err := s.db.Query(`
		SELECT id, provider, name, url, api_key, site, skip_tls_verify, created_at, updated_at
		FROM controllers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Controller
	for rows.Next() {
		var c Controller
		if err := rows.Scan(&c.ID, &c.Provider, &c.Name, &c.URL, &c.APIKey,
			&c.Site, &c.SkipTLSVerify, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Provider = normalizeProvider(c.Provider)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetController(id int64) (*Controller, error) {
	var c Controller
	err := s.db.QueryRow(`
		SELECT id, provider, name, url, api_key, site, skip_tls_verify, created_at, updated_at
		FROM controllers WHERE id = ?`, id).Scan(
		&c.ID, &c.Provider, &c.Name, &c.URL, &c.APIKey,
		&c.Site, &c.SkipTLSVerify, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.Provider = normalizeProvider(c.Provider)
	return &c, nil
}

func (s *Store) CreateController(c *Controller) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	c.Provider = normalizeProvider(c.Provider)
	if c.Site == "" {
		c.Site = "default"
	}
	result, err := s.db.Exec(`
		INSERT INTO controllers (provider, name, url, api_key, site, skip_tls_verify, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Provider, c.Name, c.URL, c.APIKey, c.Site, c.SkipTLSVerify, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdateController(c *Controller) error {
	now := time.Now().UTC().Format(time.RFC3339)
	c.Provider = normalizeProvider(c.Provider)
	_, err := s.db.Exec(`
		UPDATE controllers SET provider=?, name=?, url=?, api_key=?, site=?, skip_tls_verify=?, updated_at=?
		WHERE id=?`,
		c.Provider, c.Name, c.URL, c.APIKey, c.Site, c.SkipTLSVerify, now, c.ID)
	return err
}

func (s *Store) DeleteController(id int64) error {
	_, err := s.db.Exec("DELETE FROM controllers WHERE id = ?", id)
	return err
}

// ---------- SyncJob CRUD ----------

func (s *Store) ListJobs() ([]SyncJob, error) {
	rows, err := s.db.Query(`
		SELECT j.id, j.name, j.controller_id, j.network_list_id,
				j.hostnames, j.schedule, j.observed_ip_ttl_hours, j.enabled,
			j.last_run_at, j.last_result, j.created_at, j.updated_at,
			COALESCE(c.name, '')
		FROM sync_jobs j
		LEFT JOIN controllers c ON c.id = j.controller_id
		ORDER BY j.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []SyncJob
	for rows.Next() {
		var j SyncJob
		var enabled int
		if err := rows.Scan(&j.ID, &j.Name, &j.ControllerID, &j.NetworkListID,
			&j.Hostnames, &j.Schedule, &j.ObservedIPTTLHours, &enabled,
			&j.LastRunAt, &j.LastResult, &j.CreatedAt, &j.UpdatedAt,
			&j.ControllerName); err != nil {
			return nil, err
		}
		j.ObservedIPTTLHours = normalizeObservedIPTTLHours(j.ObservedIPTTLHours)
		j.Enabled = enabled != 0
		targets, targetsErr := s.ListJobTargets(j.ID)
		if targetsErr == nil {
			j.Targets = targets
			if len(targets) > 0 {
				j.ControllerID = targets[0].ControllerID
				j.NetworkListID = targets[0].NetworkListID
				j.ControllerName = targets[0].ControllerName
			}
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) GetJob(id int64) (*SyncJob, error) {
	var j SyncJob
	var enabled int
	err := s.db.QueryRow(`
		SELECT j.id, j.name, j.controller_id, j.network_list_id,
				j.hostnames, j.schedule, j.observed_ip_ttl_hours, j.enabled,
			j.last_run_at, j.last_result, j.created_at, j.updated_at,
			COALESCE(c.name, '')
		FROM sync_jobs j
		LEFT JOIN controllers c ON c.id = j.controller_id
		WHERE j.id = ?`, id).Scan(
		&j.ID, &j.Name, &j.ControllerID, &j.NetworkListID,
		&j.Hostnames, &j.Schedule, &j.ObservedIPTTLHours, &enabled,
		&j.LastRunAt, &j.LastResult, &j.CreatedAt, &j.UpdatedAt,
		&j.ControllerName)
	if err != nil {
		return nil, err
	}
	j.ObservedIPTTLHours = normalizeObservedIPTTLHours(j.ObservedIPTTLHours)
	j.Enabled = enabled != 0
	targets, targetsErr := s.ListJobTargets(j.ID)
	if targetsErr == nil {
		j.Targets = targets
		if len(targets) > 0 {
			j.ControllerID = targets[0].ControllerID
			j.NetworkListID = targets[0].NetworkListID
			j.ControllerName = targets[0].ControllerName
		}
	}
	return &j, nil
}

func (s *Store) CreateJob(j *SyncJob) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	j.ObservedIPTTLHours = normalizeObservedIPTTLHours(j.ObservedIPTTLHours)
	targets := normalizeTargets(j)
	if len(targets) == 0 {
		return 0, fmt.Errorf("at least one target is required")
	}
	primary := targets[0]
	result, err := s.db.Exec(`
		INSERT INTO sync_jobs (name, controller_id, network_list_id,
			hostnames, schedule, observed_ip_ttl_hours, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Name, primary.ControllerID, primary.NetworkListID,
		j.Hostnames, j.Schedule, j.ObservedIPTTLHours, boolToInt(j.Enabled),
		now, now)
	if err != nil {
		return 0, err
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := s.SetJobTargets(jobID, targets); err != nil {
		return 0, err
	}
	return jobID, nil
}

func (s *Store) UpdateJob(j *SyncJob) error {
	now := time.Now().UTC().Format(time.RFC3339)
	j.ObservedIPTTLHours = normalizeObservedIPTTLHours(j.ObservedIPTTLHours)
	targets := normalizeTargets(j)
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	primary := targets[0]
	_, err := s.db.Exec(`
		UPDATE sync_jobs SET name=?, controller_id=?, network_list_id=?,
			hostnames=?, schedule=?, observed_ip_ttl_hours=?, enabled=?, updated_at=?
		WHERE id=?`,
		j.Name, primary.ControllerID, primary.NetworkListID,
		j.Hostnames, j.Schedule, j.ObservedIPTTLHours,
		boolToInt(j.Enabled), now, j.ID)
	if err != nil {
		return err
	}
	return s.SetJobTargets(j.ID, targets)
}

func (s *Store) DeleteJob(id int64) error {
	_, err := s.db.Exec("DELETE FROM sync_jobs WHERE id = ?", id)
	return err
}

func (s *Store) UpdateJobLastRun(id int64, runAt string, result string) error {
	_, err := s.db.Exec(`
		UPDATE sync_jobs SET last_run_at=?, last_result=?, updated_at=?
		WHERE id=?`, runAt, result, runAt, id)
	return err
}

func (s *Store) CreateRunLog(l *RunLog) (int64, error) {
	result, err := s.db.Exec(`
		INSERT INTO run_logs (job_id, started_at, status, message, changes_made, details)
		VALUES (?, ?, ?, ?, ?, ?)`,
		l.JobID, l.StartedAt, l.Status, l.Message, l.ChangesMade, l.Details)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdateRunLog(l *RunLog) error {
	_, err := s.db.Exec(`
		UPDATE run_logs SET finished_at=?, status=?, message=?, changes_made=?, details=?
		WHERE id=?`,
		l.FinishedAt, l.Status, l.Message, l.ChangesMade, l.Details, l.ID)
	return err
}

func (s *Store) GetRunLogs(jobID int64, limit int) ([]RunLog, error) {
	rows, err := s.db.Query(`
		SELECT id, job_id, started_at, finished_at, status, message, changes_made, details
		FROM run_logs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?`,
		jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []RunLog
	for rows.Next() {
		var l RunLog
		if err := rows.Scan(&l.ID, &l.JobID, &l.StartedAt, &l.FinishedAt,
			&l.Status, &l.Message, &l.ChangesMade, &l.Details); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func normalizeObservedIPTTLHours(hours int) int {
	if hours < 0 {
		return DefaultObservedIPTTLHours
	}
	return hours
}

func normalizeProvider(provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider == "" {
		return "unifi"
	}
	if provider != "unifi" && provider != "npm" {
		return "unifi"
	}
	return provider
}

func normalizeTargets(job *SyncJob) []JobTarget {
	if job == nil {
		return nil
	}
	if len(job.Targets) > 0 {
		out := make([]JobTarget, 0, len(job.Targets))
		for _, t := range job.Targets {
			if t.ControllerID == 0 || strings.TrimSpace(t.NetworkListID) == "" {
				continue
			}
			t.NetworkListID = strings.TrimSpace(t.NetworkListID)
			out = append(out, t)
		}
		return out
	}
	if job.ControllerID == 0 || strings.TrimSpace(job.NetworkListID) == "" {
		return nil
	}
	return []JobTarget{{ControllerID: job.ControllerID, NetworkListID: strings.TrimSpace(job.NetworkListID)}}
}

func (s *Store) ListJobTargets(jobID int64) ([]JobTarget, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.job_id, t.controller_id, t.network_list_id,
			COALESCE(c.name, ''), COALESCE(c.provider, 'unifi')
		FROM sync_job_targets t
		LEFT JOIN controllers c ON c.id = t.controller_id
		WHERE t.job_id = ?
		ORDER BY t.id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []JobTarget
	for rows.Next() {
		var t JobTarget
		if err := rows.Scan(&t.ID, &t.JobID, &t.ControllerID, &t.NetworkListID, &t.ControllerName, &t.Provider); err != nil {
			return nil, err
		}
		t.Provider = normalizeProvider(t.Provider)
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func (s *Store) SetJobTargets(jobID int64, targets []JobTarget) error {
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM sync_job_targets WHERE job_id = ?`, jobID); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, t := range targets {
		if t.ControllerID == 0 || strings.TrimSpace(t.NetworkListID) == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO sync_job_targets (job_id, controller_id, network_list_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`,
			jobID, t.ControllerID, strings.TrimSpace(t.NetworkListID), now, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ---------- DNSServer CRUD ----------

func (s *Store) ListDNSServers() ([]DNSServer, error) {
	rows, err := s.db.Query(`SELECT id, name, address, enabled, created_at, updated_at FROM dns_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSServer
	for rows.Next() {
		var d DNSServer
		var enabled int
		if err := rows.Scan(&d.ID, &d.Name, &d.Address, &enabled, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Enabled = enabled != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDNSServer(id int64) (*DNSServer, error) {
	var d DNSServer
	var enabled int
	err := s.db.QueryRow(`SELECT id, name, address, enabled, created_at, updated_at FROM dns_servers WHERE id = ?`, id).
		Scan(&d.ID, &d.Name, &d.Address, &enabled, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	d.Enabled = enabled != 0
	return &d, nil
}

func (s *Store) CreateDNSServer(d *DNSServer) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`INSERT INTO dns_servers (name, address, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		d.Name, d.Address, boolToInt(d.Enabled), now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdateDNSServer(d *DNSServer) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`UPDATE dns_servers SET name=?, address=?, enabled=?, updated_at=? WHERE id=?`,
		d.Name, d.Address, boolToInt(d.Enabled), now, d.ID)
	return err
}

func (s *Store) DeleteDNSServer(id int64) error {
	_, err := s.db.Exec("DELETE FROM dns_servers WHERE id = ?", id)
	return err
}

// ListEnabledDNSServerAddresses returns addresses of all enabled custom DNS servers.
func (s *Store) ListEnabledDNSServerAddresses() ([]string, error) {
	rows, err := s.db.Query(`SELECT address FROM dns_servers WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var addrs []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, rows.Err()
}

// ListObservedIPs returns recently observed IPs for a job and their source labels.
func (s *Store) ListObservedIPs(jobID int64, since string) (map[string]string, error) {
	rows, err := s.db.Query(`
		SELECT ip, source
		FROM job_observed_ips
		WHERE job_id = ? AND last_seen_at >= ?
		ORDER BY ip`, jobID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var ip string
		var source string
		if err := rows.Scan(&ip, &source); err != nil {
			return nil, err
		}
		out[ip] = source
	}
	return out, rows.Err()
}

// UpsertObservedIPs records the most recent successful resolution set for a job.
func (s *Store) UpsertObservedIPs(jobID int64, hostIPs map[string]string, seenAt string) error {
	if len(hostIPs) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for ip, source := range hostIPs {
		if _, err := tx.Exec(`
			INSERT INTO job_observed_ips (job_id, ip, source, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(job_id, ip) DO UPDATE SET
				source = excluded.source,
				last_seen_at = excluded.last_seen_at`,
			jobID, ip, source, seenAt, seenAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteExpiredObservedIPs removes stale observed IPs for a job.
func (s *Store) DeleteExpiredObservedIPs(jobID int64, before string) error {
	_, err := s.db.Exec(`DELETE FROM job_observed_ips WHERE job_id = ? AND last_seen_at < ?`, jobID, before)
	return err
}

// DeleteObservedIPs removes all cached observed IPs for a job.
func (s *Store) DeleteObservedIPs(jobID int64) error {
	_, err := s.db.Exec(`DELETE FROM job_observed_ips WHERE job_id = ?`, jobID)
	return err
}
