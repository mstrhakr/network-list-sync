package clients

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewNPMClient_RequiresCredentialsAndTrimsURL(t *testing.T) {
	if _, err := NewNPMClient("https://example.com", "", "secret", false); err == nil {
		t.Fatal("expected identity error")
	}
	if _, err := NewNPMClient("https://example.com", "user", "", false); err == nil {
		t.Fatal("expected secret error")
	}
	c, err := NewNPMClient("https://example.com///", "user", "k", false)
	if err != nil {
		t.Fatalf("NewNPMClient() error = %v", err)
	}
	if c.baseURL != "https://example.com" {
		t.Fatalf("baseURL = %q", c.baseURL)
	}
}

func TestNewNPMClient_RejectsUnsafeOrInvalidBaseURL(t *testing.T) {
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
			if _, err := NewNPMClient(raw, "user", "k", false); err == nil {
				t.Fatalf("expected error for base URL %q", raw)
			}
		})
	}
}

func TestNormalizeNPMBaseURL_DefaultsToAPI(t *testing.T) {
	base, api, err := normalizeNPMBaseURL("http://192.168.1.171:81")
	if err != nil {
		t.Fatalf("normalizeNPMBaseURL() error = %v", err)
	}
	if base != "http://192.168.1.171:81" {
		t.Fatalf("base = %q", base)
	}
	if api != "/api" {
		t.Fatalf("api = %q, want /api", api)
	}
}

func TestNormalizeNPMBaseURL_AcceptsExplicitAPIPath(t *testing.T) {
	base, api, err := normalizeNPMBaseURL("http://192.168.1.171:81/api")
	if err != nil {
		t.Fatalf("normalizeNPMBaseURL() error = %v", err)
	}
	if base != "http://192.168.1.171:81" || api != "/api" {
		t.Fatalf("got base=%q api=%q", base, api)
	}
}

func TestNPMClient_ReauthOnUnauthorized(t *testing.T) {
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

	c, err := NewNPMClient(ts.URL, "user", "secret", false)
	if err != nil {
		t.Fatalf("NewNPMClient() error = %v", err)
	}
	c.httpClient = ts.Client()
	c.token = "stale"

	if _, err := c.ListNetworkLists(); err != nil {
		t.Fatalf("ListNetworkLists() with refresh error = %v", err)
	}
}
