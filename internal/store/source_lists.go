package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SourceList is a reusable ordered set of entries and nested source references.
type SourceList struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Hostnames       string  `json:"hostnames"`
	IncludedListIDs []int64 `json:"included_list_ids"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func (s *Store) migrateSourceLists() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS source_lists (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		hostnames TEXT NOT NULL,
		included_list_ids TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	rows, err := s.db.Query(`PRAGMA table_info(sync_jobs)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		found = found || name == "included_list_ids"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		_, err = s.db.Exec(`ALTER TABLE sync_jobs ADD COLUMN included_list_ids TEXT NOT NULL DEFAULT '[]'`)
	}
	return err
}

func normalizeSourceIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func decodeSourceIDs(raw string) ([]int64, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil || strings.TrimSpace(raw) == "null" {
		return nil, fmt.Errorf("invalid included_list_ids JSON array: %q", raw)
	}
	return normalizeSourceIDs(ids), nil
}

type sourceQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func listSourceLists(q sourceQuerier) ([]SourceList, error) {
	rows, err := q.Query(`SELECT id, name, hostnames, included_list_ids, created_at, updated_at
		FROM source_lists ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SourceList, 0)
	for rows.Next() {
		var source SourceList
		var raw string
		if err := rows.Scan(&source.ID, &source.Name, &source.Hostnames, &raw, &source.CreatedAt, &source.UpdatedAt); err != nil {
			return nil, err
		}
		source.IncludedListIDs, err = decodeSourceIDs(raw)
		if err != nil {
			return nil, fmt.Errorf("source list %d: %w", source.ID, err)
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func (s *Store) ListSourceLists() ([]SourceList, error) {
	return listSourceLists(s.db)
}

func (s *Store) GetSourceList(id int64) (*SourceList, error) {
	var source SourceList
	var raw string
	err := s.db.QueryRow(`SELECT id, name, hostnames, included_list_ids, created_at, updated_at
		FROM source_lists WHERE id = ?`, id).Scan(&source.ID, &source.Name, &source.Hostnames,
		&raw, &source.CreatedAt, &source.UpdatedAt)
	if err != nil {
		return nil, err
	}
	source.IncludedListIDs, err = decodeSourceIDs(raw)
	if err != nil {
		return nil, fmt.Errorf("source list %d: %w", id, err)
	}
	return &source, nil
}

// expandSources uses one snapshot, emitting inline entries first, then sources
// in depth-first include order. A shared descendant is emitted only once.
func expandSources(sources []SourceList, hostnames string, ids []int64) (string, error) {
	graph := make(map[int64]SourceList, len(sources))
	for _, source := range sources {
		graph[source.ID] = source
	}
	state := make(map[int64]uint8, len(sources))
	parts := make([]string, 0)
	appendEntries := func(entries string) {
		if entries = strings.TrimSpace(entries); entries != "" {
			parts = append(parts, entries)
		}
	}
	appendEntries(hostnames)
	var visit func(int64) error
	visit = func(id int64) error {
		if state[id] == 1 {
			return fmt.Errorf("source list cycle detected at ID %d", id)
		}
		if state[id] == 2 {
			return nil
		}
		source, ok := graph[id]
		if !ok {
			return fmt.Errorf("source list ID %d does not exist", id)
		}
		state[id] = 1
		appendEntries(source.Hostnames)
		for _, child := range source.IncludedListIDs {
			if err := visit(child); err != nil {
				return fmt.Errorf("source list %d (%s): %w", id, source.Name, err)
			}
		}
		state[id] = 2
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return "", err
		}
	}
	return strings.Join(parts, "\n"), nil
}

func validateSourceContent(hostnames string, ids []int64) error {
	if strings.TrimSpace(hostnames) == "" && len(ids) == 0 {
		return fmt.Errorf("source entries require hostnames or included source lists")
	}
	return nil
}

func validateJobSourceIDs(q sourceQuerier, ids []int64) error {
	// Preserve legacy callers that store empty inline entries without includes.
	if len(ids) == 0 {
		return nil
	}
	sources, err := listSourceLists(q)
	if err != nil {
		return err
	}
	_, err = expandSources(sources, "", ids)
	return err
}

func (s *Store) ExpandSourceEntries(hostnames string, ids []int64) (string, error) {
	sources, err := s.ListSourceLists()
	if err != nil {
		return "", err
	}
	return expandSources(sources, hostnames, ids)
}

func (s *Store) ValidateSourceEntries(hostnames string, ids []int64) error {
	if err := validateSourceContent(hostnames, ids); err != nil {
		return err
	}
	_, err := s.ExpandSourceEntries(hostnames, ids)
	return err
}

func (s *Store) CreateSourceList(source *SourceList) (int64, error) {
	return s.writeSourceList(source, false)
}

func (s *Store) UpdateSourceList(source *SourceList) error {
	_, err := s.writeSourceList(source, true)
	return err
}

func (s *Store) writeSourceList(source *SourceList, update bool) (int64, error) {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if source == nil || strings.TrimSpace(source.Name) == "" {
		return 0, fmt.Errorf("source list name is required")
	}
	value := *source
	value.Name = strings.TrimSpace(value.Name)
	value.IncludedListIDs = normalizeSourceIDs(value.IncludedListIDs)
	if err := validateSourceContent(value.Hostnames, value.IncludedListIDs); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(value.IncludedListIDs)
	if update {
		if err := tx.QueryRow(`SELECT created_at FROM source_lists WHERE id = ?`, value.ID).Scan(&value.CreatedAt); err != nil {
			return 0, err
		}
		_, err = tx.Exec(`UPDATE source_lists SET name=?, hostnames=?, included_list_ids=?, updated_at=? WHERE id=?`,
			value.Name, value.Hostnames, string(raw), value.UpdatedAt, value.ID)
	} else {
		value.CreatedAt = value.UpdatedAt
		var result sql.Result
		result, err = tx.Exec(`INSERT INTO source_lists (name, hostnames, included_list_ids, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, value.Name, value.Hostnames, string(raw), value.CreatedAt, value.UpdatedAt)
		if err == nil {
			value.ID, err = result.LastInsertId()
		}
	}
	if err != nil {
		return 0, err
	}
	sources, err := listSourceLists(tx)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(sources))
	for _, item := range sources {
		ids = append(ids, item.ID)
	}
	if _, err := expandSources(sources, "", ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	*source = value
	return value.ID, nil
}

func (s *Store) DeleteSourceList(id int64) error {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sources, err := listSourceLists(tx)
	if err != nil {
		return err
	}
	for _, source := range sources {
		for _, ref := range source.IncludedListIDs {
			if ref == id {
				return fmt.Errorf("source list %d is referenced by source list %d (%s)", id, source.ID, source.Name)
			}
		}
	}
	rows, err := tx.Query(`SELECT id, name, included_list_ids FROM sync_jobs ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID int64
		var name, raw string
		if err := rows.Scan(&jobID, &name, &raw); err != nil {
			return err
		}
		ids, err := decodeSourceIDs(raw)
		if err != nil {
			return fmt.Errorf("job %d: %w", jobID, err)
		}
		for _, ref := range ids {
			if ref == id {
				return fmt.Errorf("source list %d is referenced by job %d (%s)", id, jobID, name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM source_lists WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
