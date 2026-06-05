package clients

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrafficMatchItemUnmarshalJSON_AcceptsStringAndNumberScalars(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantVal string
		wantSt  string
		wantSp  string
	}{
		{
			name:    "string value",
			input:   `{"type":"IP_ADDRESS","value":"192.168.1.5"}`,
			wantVal: "192.168.1.5",
		},
		{
			name:    "numeric value",
			input:   `{"type":"PORT_NUMBER","value":443}`,
			wantVal: "443",
		},
		{
			name:   "numeric range",
			input:  `{"type":"PORT_NUMBER_RANGE","start":1000,"stop":2000}`,
			wantSt: "1000",
			wantSp: "2000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var item TrafficMatchItem
			if err := json.Unmarshal([]byte(tc.input), &item); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if item.Value != tc.wantVal {
				t.Fatalf("Value = %q, want %q", item.Value, tc.wantVal)
			}
			if item.Start != tc.wantSt {
				t.Fatalf("Start = %q, want %q", item.Start, tc.wantSt)
			}
			if item.Stop != tc.wantSp {
				t.Fatalf("Stop = %q, want %q", item.Stop, tc.wantSp)
			}
		})
	}
}

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

func TestNewUniFiClient_RequiresAPIKeyAndTrimsURL(t *testing.T) {
	if _, err := NewUniFiClient("https://example.com", "default", "", false); err == nil {
		t.Fatal("expected API key error")
	}
	c, err := NewUniFiClient("https://example.com///", "default", "k", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
	}
	if c.baseURL != "https://example.com" {
		t.Fatalf("baseURL = %q", c.baseURL)
	}
}

func TestNewUniFiClient_RejectsUnsafeOrInvalidBaseURL(t *testing.T) {
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
			if _, err := NewUniFiClient(raw, "default", "k", false); err == nil {
				t.Fatalf("expected error for base URL %q", raw)
			}
		})
	}
}

func TestUniFiDoRequest_StatusHTMLAndSuccess(t *testing.T) {
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

	c, err := NewUniFiClient(ts.URL, "default", "key", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
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
