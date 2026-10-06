package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func sourceTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "sources.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func createTestSource(t *testing.T, s *Store, name, entries string, ids ...int64) *SourceList {
	t.Helper()
	source := &SourceList{Name: name, Hostnames: entries, IncludedListIDs: ids}
	id, err := s.CreateSourceList(source)
	if err != nil {
		t.Fatal(err)
	}
	if id != source.ID {
		t.Fatalf("returned ID %d != source ID %d", id, source.ID)
	}
	return source
}

func sourceTestJob(t *testing.T, s *Store, ids ...int64) *SyncJob {
	t.Helper()
	controllerID, err := s.CreateController(&Controller{Name: "controller", URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return &SyncJob{Name: "job", ControllerID: controllerID, NetworkListID: "target", IncludedListIDs: ids}
}

func TestSourceListsCRUDAndOrderedDAG(t *testing.T) {
	s := sourceTestStore(t)
	items, err := s.ListSourceLists()
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty list = %v, %v", items, err)
	}
	leaf := createTestSource(t, s, " leaf ", "leaf.example\n192.0.2.0/24")
	left := createTestSource(t, s, "left", "left.example", leaf.ID, leaf.ID)
	right := createTestSource(t, s, "right", "right.example", leaf.ID)
	root := createTestSource(t, s, "root", "", left.ID, right.ID, left.ID)
	if leaf.Name != "leaf" || leaf.CreatedAt == "" || leaf.UpdatedAt == "" || leaf.IncludedListIDs == nil {
		t.Fatalf("source normalization = %+v", leaf)
	}
	if !reflect.DeepEqual(root.IncludedListIDs, []int64{left.ID, right.ID}) {
		t.Fatalf("normalized root refs = %v", root.IncludedListIDs)
	}
	got, err := s.ExpandSourceEntries(" inline.example \n", []int64{root.ID, leaf.ID, root.ID})
	want := "inline.example\nleft.example\nleaf.example\n192.0.2.0/24\nright.example"
	if err != nil || got != want {
		t.Fatalf("expansion = %q, %v; want %q", got, err, want)
	}
	if err := s.ValidateSourceEntries("", []int64{root.ID}); err != nil {
		t.Fatal(err)
	}
	before := leaf.CreatedAt
	leaf.Name, leaf.Hostnames = " renamed ", "new.example"
	if err := s.UpdateSourceList(leaf); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetSourceList(leaf.ID)
	if err != nil || stored.Name != "renamed" || stored.CreatedAt != before || stored.UpdatedAt == before {
		t.Fatalf("updated source = %+v, %v", stored, err)
	}
	got, err = s.ExpandSourceEntries("", []int64{root.ID})
	if err != nil || got != "left.example\nnew.example\nright.example" {
		t.Fatalf("fresh expansion = %q, %v", got, err)
	}
	items, err = s.ListSourceLists()
	if err != nil || len(items) != 4 || items[0].Name != "left" {
		t.Fatalf("list = %+v, %v", items, err)
	}
	if err := s.DeleteSourceList(root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSourceList(root.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted source error = %v", err)
	}
	if err := s.UpdateSourceList(root); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing update error = %v", err)
	}
}

func TestSourceListsValidationAndRollback(t *testing.T) {
	s := sourceTestStore(t)
	for _, source := range []*SourceList{
		nil, {Name: "  ", Hostnames: "example.com"}, {Name: "empty", Hostnames: " \n"},
		{Name: "missing", IncludedListIDs: []int64{99}},
		{Name: "negative", IncludedListIDs: []int64{-1}},
		{Name: "zero", IncludedListIDs: []int64{0}},
		{Name: "self", IncludedListIDs: []int64{1}},
	} {
		if _, err := s.CreateSourceList(source); err == nil {
			t.Fatalf("accepted invalid source %+v", source)
		}
	}
	items, err := s.ListSourceLists()
	if err != nil || len(items) != 0 {
		t.Fatalf("failed creates persisted: %v, %v", items, err)
	}
	a := createTestSource(t, s, "A", "a.example")
	b := createTestSource(t, s, "B", "", a.ID)
	c := createTestSource(t, s, "C", "", b.ID)
	for _, refs := range [][]int64{{a.ID}, {c.ID}, {999}} {
		a.IncludedListIDs = refs
		if err := s.UpdateSourceList(a); err == nil {
			t.Fatalf("accepted invalid refs %v", refs)
		}
		stored, err := s.GetSourceList(a.ID)
		if err != nil || len(stored.IncludedListIDs) != 0 {
			t.Fatalf("failed update persisted: %+v, %v", stored, err)
		}
	}
	if err := s.ValidateSourceEntries(" \n", nil); err == nil {
		t.Fatal("accepted empty entries")
	}
	if err := s.ValidateSourceEntries("inline.example", []int64{999}); err == nil {
		t.Fatal("inline content masked missing reference")
	}
	if err := s.ValidateSourceEntries("inline.example", nil); err != nil {
		t.Fatal(err)
	}
	// Simulate corrupt persisted graphs: expansion must return no partial content.
	for _, raw := range []string{fmt.Sprintf("[%d]", c.ID), "[999]", "invalid", "null", "{}"} {
		if _, err := s.db.Exec(`UPDATE source_lists SET included_list_ids=? WHERE id=?`, raw, a.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.ExpandSourceEntries("inline.example", []int64{c.ID})
		if err == nil || got != "" {
			t.Fatalf("corrupt graph %q expanded to %q, %v", raw, got, err)
		}
		if err := s.ValidateSourceEntries("", []int64{c.ID}); err == nil {
			t.Fatalf("validated corrupt graph %q", raw)
		}
	}
}

func TestSourceListsJobReferencesAndDeleteGuards(t *testing.T) {
	s := sourceTestStore(t)
	leaf := createTestSource(t, s, "leaf", "leaf.example")
	root := createTestSource(t, s, "root", "", leaf.ID)
	if err := s.DeleteSourceList(leaf.ID); err == nil || !strings.Contains(err.Error(), "source list") || !strings.Contains(err.Error(), "root") {
		t.Fatalf("source delete guard = %v", err)
	}
	job := sourceTestJob(t, s, root.ID, root.ID)
	id, err := s.CreateJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.ID = id
	stored, err := s.GetJob(id)
	if err != nil || !reflect.DeepEqual(stored.IncludedListIDs, []int64{root.ID}) {
		t.Fatalf("job refs = %+v, %v", stored, err)
	}
	jobs, err := s.ListJobs()
	if err != nil || len(jobs) != 1 || !reflect.DeepEqual(jobs[0].IncludedListIDs, []int64{root.ID}) {
		t.Fatalf("listed job refs = %+v, %v", jobs, err)
	}
	if err := s.DeleteSourceList(root.ID); err == nil || !strings.Contains(err.Error(), "job") || !strings.Contains(err.Error(), job.Name) {
		t.Fatalf("job delete guard = %v", err)
	}
	job.IncludedListIDs = []int64{leaf.ID, leaf.ID}
	if err := s.UpdateJob(job); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetJob(id)
	if err != nil || !reflect.DeepEqual(stored.IncludedListIDs, []int64{leaf.ID}) {
		t.Fatalf("updated job refs = %+v, %v", stored, err)
	}
	if err := s.DeleteSourceList(root.ID); err != nil {
		t.Fatal(err)
	}
	job.IncludedListIDs = nil
	if err := s.UpdateJob(job); err != nil {
		t.Fatalf("legacy empty-hostnames update: %v", err)
	}
	if err := s.DeleteSourceList(leaf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(sourceTestJob(t, s)); err != nil {
		t.Fatalf("legacy empty-hostnames create: %v", err)
	}
	job.IncludedListIDs = []int64{leaf.ID}
	if _, err := s.CreateJob(job); err == nil {
		t.Fatal("created job with missing source")
	}
	if err := s.UpdateJob(job); err == nil {
		t.Fatal("updated job with missing source")
	}
	stored, err = s.GetJob(id)
	if err != nil || len(stored.IncludedListIDs) != 0 {
		t.Fatalf("invalid update persisted: %+v, %v", stored, err)
	}
	encoded, err := json.Marshal(stored)
	if err != nil || !strings.Contains(string(encoded), `"included_list_ids":[]`) {
		t.Fatalf("empty refs JSON = %s, %v", encoded, err)
	}
}

func TestSourceListsJobTargetsAtomic(t *testing.T) {
	s := sourceTestStore(t)
	a := createTestSource(t, s, "A", "a.example")
	b := createTestSource(t, s, "B", "b.example")
	job := sourceTestJob(t, s, a.ID)
	duplicateTargets := []JobTarget{
		{ControllerID: job.ControllerID, NetworkListID: "duplicate"},
		{ControllerID: job.ControllerID, NetworkListID: "duplicate"},
	}
	job.Targets = duplicateTargets
	if _, err := s.CreateJob(job); err == nil {
		t.Fatal("expected target uniqueness failure")
	}
	jobs, err := s.ListJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("failed create left job: %+v, %v", jobs, err)
	}
	job.Targets = nil
	job.ID, err = s.CreateJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Name, job.IncludedListIDs, job.Targets = "changed", []int64{b.ID}, duplicateTargets
	if err := s.UpdateJob(job); err == nil {
		t.Fatal("expected target uniqueness failure on update")
	}
	stored, err := s.GetJob(job.ID)
	if err != nil || stored.Name != "job" || !reflect.DeepEqual(stored.IncludedListIDs, []int64{a.ID}) ||
		len(stored.Targets) != 1 || stored.Targets[0].NetworkListID != "target" {
		t.Fatalf("failed update changed job/refs/targets: %+v, %v", stored, err)
	}
}

func TestSourceListsMigrationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sync_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
		controller_id INTEGER NOT NULL, network_list_id TEXT NOT NULL,
		hostnames TEXT NOT NULL, schedule TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
		last_run_at TEXT, last_result TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	);
	INSERT INTO sync_jobs (name, controller_id, network_list_id, hostnames, created_at, updated_at)
	VALUES ('legacy', 1, 'target', '', 'old', 'old')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob(1)
	if err != nil || job.IncludedListIDs == nil || len(job.IncludedListIDs) != 0 {
		t.Fatalf("migrated job = %+v, %v", job, err)
	}
	controllerID, err := s.CreateController(&Controller{Name: "legacy controller"})
	if err != nil {
		t.Fatal(err)
	}
	leaf := createTestSource(t, s, "leaf", "leaf.example")
	root := createTestSource(t, s, "root", "root.example", leaf.ID)
	job.ControllerID, job.IncludedListIDs = controllerID, []int64{root.ID}
	if err := s.UpdateJob(job); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err = New(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ExpandSourceEntries("", []int64{root.ID})
		if err != nil || got != "root.example\nleaf.example" {
			t.Fatalf("reopened expansion = %q, %v", got, err)
		}
		job, err = s.GetJob(1)
		if err != nil || !reflect.DeepEqual(job.IncludedListIDs, []int64{root.ID}) {
			t.Fatalf("reopened job = %+v, %v", job, err)
		}
		s.Close()
	}
}

func TestSourceListsConcurrentEdits(t *testing.T) {
	s := sourceTestStore(t)
	for i := 0; i < 10; i++ {
		a := createTestSource(t, s, fmt.Sprintf("A%d", i), "a.example")
		b := createTestSource(t, s, fmt.Sprintf("B%d", i), "b.example")
		a.IncludedListIDs, b.IncludedListIDs = []int64{b.ID}, []int64{a.ID}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, source := range []*SourceList{a, b} {
			go func(source *SourceList) { <-start; results <- s.UpdateSourceList(source) }(source)
		}
		close(start)
		first, second := <-results, <-results
		if (first == nil) == (second == nil) {
			t.Fatalf("concurrent cycle edits = %v, %v; want exactly one success", first, second)
		}
		if _, err := s.ExpandSourceEntries("", []int64{a.ID, b.ID}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		leaf := createTestSource(t, s, fmt.Sprintf("leaf%d", i), "leaf.example")
		job := sourceTestJob(t, s, leaf.ID)
		var wg sync.WaitGroup
		wg.Add(2)
		var jobErr, deleteErr error
		go func() { defer wg.Done(); _, jobErr = s.CreateJob(job) }()
		go func() { defer wg.Done(); deleteErr = s.DeleteSourceList(leaf.ID) }()
		wg.Wait()
		if (jobErr == nil) == (deleteErr == nil) {
			t.Fatalf("concurrent job/delete = %v, %v; want exactly one success", jobErr, deleteErr)
		}
	}
}

func TestSourceListsSharedDatabaseTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Different Store mutexes cannot protect each other. SQLite transactions
	// must still prevent both cyclic edits or a reference plus deletion committing.
	for i := 0; i < 10; i++ {
		a := createTestSource(t, first, fmt.Sprintf("shared A%d", i), "a.example")
		b := createTestSource(t, second, fmt.Sprintf("shared B%d", i), "b.example")
		a.IncludedListIDs, b.IncludedListIDs = []int64{b.ID}, []int64{a.ID}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- first.UpdateSourceList(a) }()
		go func() { <-start; results <- second.UpdateSourceList(b) }()
		close(start)
		one, two := <-results, <-results
		if one == nil && two == nil {
			t.Fatal("both cyclic edits committed across Store instances")
		}
		if _, err := first.ExpandSourceEntries("", []int64{a.ID, b.ID}); err != nil {
			t.Fatalf("persisted graph invalid: %v", err)
		}
		leaf := createTestSource(t, first, fmt.Sprintf("shared leaf%d", i), "leaf.example")
		parent := &SourceList{Name: fmt.Sprintf("shared parent%d", i), IncludedListIDs: []int64{leaf.ID}}
		start = make(chan struct{})
		go func() { <-start; _, err := first.CreateSourceList(parent); results <- err }()
		go func() { <-start; results <- second.DeleteSourceList(leaf.ID) }()
		close(start)
		one, two = <-results, <-results
		if one == nil && two == nil {
			t.Fatal("reference and deletion both committed across Store instances")
		}
		if parent.ID != 0 {
			if _, err := first.ExpandSourceEntries("", []int64{parent.ID}); err != nil {
				t.Fatalf("persisted dangling source reference: %v", err)
			}
		}
		job := sourceTestJob(t, first, b.ID)
		start = make(chan struct{})
		go func() { <-start; _, err := first.CreateJob(job); results <- err }()
		go func() { <-start; results <- second.DeleteSourceList(b.ID) }()
		close(start)
		one, two = <-results, <-results
		if one == nil && two == nil {
			t.Fatal("job reference and deletion both committed across Store instances")
		}
		jobs, err := first.ListJobs()
		if err != nil {
			t.Fatal(err)
		}
		for _, stored := range jobs {
			if err := first.ValidateSourceEntries(stored.Hostnames, stored.IncludedListIDs); err != nil {
				t.Fatalf("persisted dangling job reference: %v", err)
			}
		}
	}
}

func TestSourceListJSONContract(t *testing.T) {
	s := sourceTestStore(t)
	source := createTestSource(t, s, "JSON", "example.com")
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "name", "hostnames", "included_list_ids", "created_at", "updated_at"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing JSON field %q in %s", name, raw)
		}
	}
	if len(fields) != 6 || string(fields["included_list_ids"]) != "[]" {
		t.Fatalf("source JSON contract = %s", raw)
	}
}

