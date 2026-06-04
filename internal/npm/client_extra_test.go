package npm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_RequiresAPIKeyAndTrimsURL(t *testing.T) {
	if _, err := NewClient("https://example.com", "", "secret", false); err == nil {
		t.Fatal("expected identity error")
	}
	if _, err := NewClient("https://example.com", "user", "", false); err == nil {
		t.Fatal("expected secret error")
	}
	c, err := NewClient("https://example.com///", "user", "k", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.baseURL != "https://example.com" {
		t.Fatalf("baseURL = %q", c.baseURL)
	}
}

func TestNewClient_RejectsUnsafeOrInvalidBaseURL(t *testing.T) {
	tests := []string{
		"",
		"ftp://example.com",
		"https://user:pass@example.com",
		"https://example.com/path/not-allowed",
		"https://example.com?x=1",
		"https://example.com/#frag",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := NewClient(raw, "user", "k", false); err == nil {
				t.Fatalf("expected error for base URL %q", raw)
			}
		})
	}
}

func TestClient_ReauthOnUnauthorized(t *testing.T) {
	authorized := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tokens":
			authorized = true
			_, _ = io.WriteString(w, `{"token":"refreshed"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/nginx/access-lists":
			if !authorized || r.Header.Get("Authorization") != "Bearer refreshed" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := NewClient(ts.URL, "user", "secret", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.httpClient = ts.Client()
	c.token = "stale"

	if _, err := c.ListNetworkLists(); err != nil {
		t.Fatalf("ListNetworkLists() with refresh error = %v", err)
	}
}
