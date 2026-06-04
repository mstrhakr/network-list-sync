package npm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_AccessListFlow(t *testing.T) {
	var updated accessListUpdateRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tokens":
			_, _ = io.WriteString(w, `{"token":"tkn"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/nginx/access-lists":
			if r.Header.Get("Authorization") != "Bearer tkn" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `[{"id":2,"name":"RFC1918"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/nginx/access-lists/2":
			_, _ = io.WriteString(w, `{"id":2,"name":"RFC1918","satisfy_any":false,"pass_auth":false,"items":[],"clients":[{"address":"10.0.0.0/8","directive":"allow"},{"address":"203.0.113.0/24","directive":"deny"}]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/nginx/access-lists/2":
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := NewClient(ts.URL, "user@example.com", "secret", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.httpClient = ts.Client()

	lists, err := c.ListNetworkLists()
	if err != nil {
		t.Fatalf("ListNetworkLists() error = %v", err)
	}
	if len(lists) != 1 || lists[0].ID != "2" {
		t.Fatalf("ListNetworkLists() = %+v", lists)
	}

	nl, err := c.GetNetworkList("2")
	if err != nil {
		t.Fatalf("GetNetworkList() error = %v", err)
	}
	if len(nl.Items) != 1 || nl.Items[0].Value != "10.0.0.0/8" {
		t.Fatalf("GetNetworkList() mapped items = %+v", nl.Items)
	}

	nl.Items = []TrafficMatchItem{{Type: "IP_ADDRESS", Value: "198.51.100.10"}}
	if err := c.UpdateNetworkList(nl); err != nil {
		t.Fatalf("UpdateNetworkList() error = %v", err)
	}

	if len(updated.Clients) != 2 {
		t.Fatalf("updated clients count = %d, want 2", len(updated.Clients))
	}
	if updated.Clients[0].Directive != "deny" || updated.Clients[1].Address != "198.51.100.10" {
		t.Fatalf("updated clients = %+v", updated.Clients)
	}
}
