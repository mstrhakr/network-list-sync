package syncer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mstrhakr/network-list-sync/internal/clients"
	"github.com/mstrhakr/network-list-sync/internal/store"
)

type sourceSyncFixture struct {
	db           *store.Store
	dbPath       string
	controllerID int64
	mu           sync.Mutex
	requests     int
	payloads     []json.RawMessage
	list         clients.NetworkList
}

func newSourceSyncFixture(t *testing.T) *sourceSyncFixture {
	t.Helper()
	f := &sourceSyncFixture{
		dbPath: filepath.Join(t.TempDir(), "sync.db"),
		list:   clients.NetworkList{ID: "list-1", Name: "Allowlist", Type: "IPV4_ADDRESSES"},
	}
	var err error
	f.db, err = store.New(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	// Remove seeded public resolvers. All inputs are literals, so DNS is never
	// needed; even an accidental lookup can only reach loopback.
	servers, err := f.db.ListDNSServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		if err := f.db.DeleteDNSServer(server.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.CreateDNSServer(&store.DNSServer{
		Name: "Unused loopback resolver", Address: "127.0.0.1:1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	const siteID = "11111111-1111-1111-1111-111111111111"
	const apiBase = "/proxy/network/integration/v1"
	listPath := apiBase + "/sites/" + siteID + "/traffic-matching-lists/list-1"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("provider API key = %q", r.Header.Get("X-API-Key"))
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiBase+"/sites":
			switch r.URL.Query().Get("limit") {
			case "1":
				_, _ = fmt.Fprint(w, `{"offset":0,"limit":1,"count":0,"totalCount":0,"data":[]}`)
			case "200":
				_, _ = fmt.Fprintf(w, `{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[{"id":%q,"internalReference":"default","name":"Default"}]}`, siteID)
			default:
				t.Errorf("unexpected sites query: %s", r.URL)
				http.Error(w, "unexpected query", http.StatusBadRequest)
			}
		case r.Method == http.MethodGet && r.URL.Path == listPath:
			if err := json.NewEncoder(w).Encode(f.list); err != nil {
				t.Errorf("encode provider list: %v", err)
			}
		case r.Method == http.MethodPut && r.URL.Path == listPath:
			var raw json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("decode provider payload: %v", err)
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			var update clients.NetworkList
			if err := json.Unmarshal(raw, &update); err != nil {
				t.Errorf("decode provider items: %v", err)
				http.Error(w, "invalid list", http.StatusBadRequest)
				return
			}
			f.payloads = append(f.payloads, raw)
			f.list.Name, f.list.Type, f.list.Items = update.Name, update.Type, update.Items
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	f.controllerID, err = f.db.CreateController(&store.Controller{
		Provider: "unifi", Name: "Loopback UniFi", URL: ts.URL, Site: "default", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *sourceSyncFixture) source(t *testing.T, name, entries string, ids ...int64) *store.SourceList {
	t.Helper()
	source := &store.SourceList{Name: name, Hostnames: entries, IncludedListIDs: ids}
	if _, err := f.db.CreateSourceList(source); err != nil {
		t.Fatal(err)
	}
	return source
}

func (f *sourceSyncFixture) job(t *testing.T, entries string, ids ...int64) *store.SyncJob {
	t.Helper()
	job := &store.SyncJob{
		Name: "Source integration", ControllerID: f.controllerID, NetworkListID: f.list.ID,
		Hostnames: entries, IncludedListIDs: ids, ObservedIPTTLHours: 0, Enabled: true,
	}
	var err error
	job.ID, err = f.db.CreateJob(job)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *sourceSyncFixture) assertPayload(t *testing.T, count int, want []clients.TrafficMatchItem) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) != count {
		t.Fatalf("provider writes = %d, want %d", len(f.payloads), count)
	}
	raw := f.payloads[count-1]
	var payload struct {
		Type  string                     `json:"type"`
		Name  string                     `json:"name"`
		Items []clients.TrafficMatchItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != "IPV4_ADDRESSES" || payload.Name != "Allowlist" || !reflect.DeepEqual(payload.Items, want) {
		t.Fatalf("provider payload = %s, want flat items %+v", raw, want)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 {
		t.Fatalf("provider payload must contain only type/name/items, got %s", raw)
	}
	// Check the wire shape, not merely a decoder that ignores unknown fields.
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(fields["items"], &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if len(item) != 2 || item["type"] == nil || item["value"] == nil {
			t.Fatalf("non-flat provider item: %v", item)
		}
	}
}

func TestSourceListsRunNestedFlatPayload(t *testing.T) {
	f := newSourceSyncFixture(t)
	leaf := f.source(t, "RFC1918", "10.0.0.0/8\n172.16.0.0/12\n192.168.0.0/16")
	left := f.source(t, "Left", "10.1.2.3\n10.0.0.0/8", leaf.ID)
	right := f.source(t, "Right", "192.168.1.0/24\n203.0.113.10", leaf.ID)
	root := f.source(t, "Root", "172.16.1.1", left.ID, right.ID)
	job := f.job(t, "203.0.113.10\n203.0.113.10", root.ID, leaf.ID)

	result := New().Run(f.db, job.ID)
	if result.Status != "success" || result.ChangesMade != 4 {
		t.Fatalf("Run = %+v", result)
	}
	want := []clients.TrafficMatchItem{
		{Type: "SUBNET", Value: "10.0.0.0/8"},
		{Type: "SUBNET", Value: "172.16.0.0/12"},
		{Type: "SUBNET", Value: "192.168.0.0/16"},
		{Type: "IP_ADDRESS", Value: "203.0.113.10"},
	}
	f.assertPayload(t, 1, want)
	// Shared descendants are expanded once; repeated entries still contribute
	// to duplicate statistics before covered addresses/subnets are removed.
	if result.Stats.InputEntries != 10 || result.Stats.UniqueEntries != 7 ||
		result.Stats.DuplicateEntries != 3 || result.Stats.CoveredEntriesRemoved != 3 || result.Stats.OutputEntries != 4 {
		t.Fatalf("nested optimization stats = %+v", result.Stats)
	}
	if len(result.Targets) != 1 || result.Targets[0].EntryCount != len(want) {
		t.Fatalf("sent target snapshots = %+v", result.Targets)
	}
	var snapshot []clients.TrafficMatchItem
	if err := json.Unmarshal(result.Targets[0].Items, &snapshot); err != nil || !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("sent snapshot = %+v, error = %v", snapshot, err)
	}
	// The mock retains writes, so the same job must not send a second PUT.
	if again := New().Run(f.db, job.ID); again.Status != "success" || again.ChangesMade != 0 {
		t.Fatalf("unchanged Run = %+v", again)
	}
	f.assertPayload(t, 1, want)
}

func TestSourceListsRunIncludesOnlyUsesSourceEdits(t *testing.T) {
	f := newSourceSyncFixture(t)
	leaf := f.source(t, "Editable", "10.0.0.0/8\n203.0.113.10")
	root := f.source(t, "Includes only", "", leaf.ID)
	job := f.job(t, "", root.ID)
	syn := New()
	if result := syn.Run(f.db, job.ID); result.Status != "success" {
		t.Fatalf("includes-only Run = %+v", result)
	}
	f.assertPayload(t, 1, []clients.TrafficMatchItem{
		{Type: "SUBNET", Value: "10.0.0.0/8"}, {Type: "IP_ADDRESS", Value: "203.0.113.10"},
	})
	leaf.Hostnames = "192.168.0.0/16\n203.0.113.20"
	if err := f.db.UpdateSourceList(leaf); err != nil {
		t.Fatal(err)
	}
	if result := syn.Run(f.db, job.ID); result.Status != "success" || result.ChangesMade != 4 {
		t.Fatalf("Run after source edit = %+v", result)
	}
	f.assertPayload(t, 2, []clients.TrafficMatchItem{
		{Type: "SUBNET", Value: "192.168.0.0/16"}, {Type: "IP_ADDRESS", Value: "203.0.113.20"},
	})
	saved, err := f.db.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Hostnames != "" || saved.ObservedIPTTLHours != 0 || !reflect.DeepEqual(saved.IncludedListIDs, []int64{root.ID}) {
		t.Fatalf("source edit changed job configuration: %+v", saved)
	}
}

func (f *sourceSyncFixture) assertExpansionFailure(t *testing.T, result SyncResult, message string) {
	t.Helper()
	if result.Status != "error" || !strings.HasPrefix(result.Message, "expand source lists: ") ||
		!strings.Contains(result.Message, message) || result.ChangesMade != 0 || len(result.Targets) != 0 {
		t.Fatalf("expected expansion failure containing %q, got %+v", message, result)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requests != 0 || len(f.payloads) != 0 {
		t.Fatalf("invalid sources reached provider: requests=%d writes=%d", f.requests, len(f.payloads))
	}
}

func TestSourceListsExecuteMissingReferenceBeforeDNSOrProvider(t *testing.T) {
	f := newSourceSyncFixture(t)
	// A synthetic job bypasses CreateJob's missing-reference safeguards.
	job := &store.SyncJob{
		ControllerID: f.controllerID, NetworkListID: f.list.ID,
		Hostnames: "203.0.113.10", IncludedListIDs: []int64{99999}, ObservedIPTTLHours: 0,
	}
	servers, err := f.db.ListDNSServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		if err := f.db.DeleteDNSServer(server.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.assertExpansionFailure(t, New().execute(f.db, job), "source list ID 99999 does not exist")
}

func TestSourceListsRunCorruptReferencesBeforeProvider(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		message string
	}{
		{name: "malformed JSON", raw: "invalid", message: "invalid included_list_ids JSON array"},
		{name: "null refs", raw: "null", message: "invalid included_list_ids JSON array"},
		{name: "object refs", raw: "{}", message: "invalid included_list_ids JSON array"},
		{name: "missing nested ref", raw: "[99999]", message: "source list ID 99999 does not exist"},
		{name: "nested cycle", message: "source list cycle detected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceSyncFixture(t)
			leaf := f.source(t, "Leaf", "10.0.0.0/8")
			root := f.source(t, "Root", "", leaf.ID)
			job := f.job(t, "203.0.113.10", root.ID)
			if tc.name == "nested cycle" {
				tc.raw = fmt.Sprintf("[%d]", root.ID)
			}
			// Normal store writes reject bad references and cycles. Deliberately
			// corrupt persisted data to exercise the syncer's defensive path.
			rawDB, err := sql.Open("sqlite", f.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rawDB.Close() })
			if _, err := rawDB.Exec("UPDATE source_lists SET included_list_ids = ? WHERE id = ?", tc.raw, leaf.ID); err != nil {
				t.Fatal(err)
			}
			result := New().Run(f.db, job.ID)
			f.assertExpansionFailure(t, result, tc.message)
			logs, err := f.db.GetRunLogs(job.ID, 10)
			if err != nil || len(logs) != 1 || logs[0].Status != "error" || logs[0].Message != result.Message {
				t.Fatalf("failed run logs = %+v, error = %v", logs, err)
			}
		})
	}
}

func TestSourceListsCycleSafeguardPreservesRunnableGraph(t *testing.T) {
	f := newSourceSyncFixture(t)
	leaf := f.source(t, "Leaf", "10.0.0.0/8")
	root := f.source(t, "Root", "", leaf.ID)
	job := f.job(t, "", root.ID)
	for _, id := range []int64{leaf.ID, root.ID} {
		leaf.IncludedListIDs = []int64{id}
		if err := f.db.UpdateSourceList(leaf); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("accepted cycle referencing %d: %v", id, err)
		}
	}
	f.mu.Lock()
	requests, writes := f.requests, len(f.payloads)
	f.mu.Unlock()
	if requests != 0 || writes != 0 {
		t.Fatalf("cycle validation reached provider: requests=%d writes=%d", requests, writes)
	}
	if result := New().Run(f.db, job.ID); result.Status != "success" {
		t.Fatalf("Run after rejected cycles = %+v", result)
	}
	f.assertPayload(t, 1, []clients.TrafficMatchItem{{Type: "SUBNET", Value: "10.0.0.0/8"}})
}
