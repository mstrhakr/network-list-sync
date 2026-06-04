package unifi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScalarToString_ExtraPaths(t *testing.T) {
	if got, err := scalarToString(nil); err != nil || got != "" {
		t.Fatalf("scalarToString(nil) = %q err=%v", got, err)
	}
	if got, err := scalarToString(bytes.TrimSpace([]byte("null"))); err != nil || got != "" {
		t.Fatalf("scalarToString(null) = %q err=%v", got, err)
	}
	if got, err := scalarToString([]byte("true")); err != nil || got != "true" {
		t.Fatalf("scalarToString(true) = %q err=%v", got, err)
	}
	if _, err := scalarToString([]byte("{}")); err == nil {
		t.Fatal("expected unsupported scalar type error")
	}
}

func TestNewClient_RequiresAPIKeyAndTrimsURL(t *testing.T) {
	if _, err := NewClient("https://example.com", "default", "", false); err == nil {
		t.Fatal("expected API key error")
	}
	c, err := NewClient("https://example.com///", "default", "k", false)
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
		"https://example.com/path",
		"https://example.com?x=1",
		"https://example.com/#frag",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := NewClient(raw, "default", "k", false); err == nil {
				t.Fatalf("expected error for base URL %q", raw)
			}
		})
	}
}

func TestDoRequest_StatusHTMLAndSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>login</html>"))
		default:
			http.Error(w, "bad request", http.StatusBadRequest)
		}
	}))
	defer ts.Close()

	c, err := NewClient(ts.URL, "default", "key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.httpClient = ts.Client()

	body, err := c.doRequest(http.MethodGet, "/ok", nil)
	if err != nil {
		t.Fatalf("doRequest(/ok) error = %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("doRequest(/ok) body = %q", string(body))
	}

	if _, err := c.doRequest(http.MethodGet, "/html", nil); err == nil || !strings.Contains(err.Error(), "returned HTML") {
		t.Fatalf("doRequest(/html) err = %v", err)
	}

	if _, err := c.doRequest(http.MethodGet, "/bad", nil); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("doRequest(/bad) err = %v", err)
	}
}

func TestDoRequest_RejectsNonLocalPathInputs(t *testing.T) {
	c, err := NewClient("https://example.com", "default", "k", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, err := c.doRequest(http.MethodGet, "integration/v1/sites", nil); err == nil {
		t.Fatal("expected error for path without leading slash")
	}
	if _, err := c.doRequest(http.MethodGet, "/https://evil.example/", nil); err == nil {
		t.Fatal("expected error for path containing URL scheme")
	}
}
