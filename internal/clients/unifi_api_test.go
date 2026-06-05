package clients

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUniFiClient_APIFlow_ProxyPath(t *testing.T) {
	const (
		siteID = "11111111-1111-1111-1111-111111111111"
		listID = "list-1"
	)

	var updatedPayload []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/network/integration/v1/sites" && r.URL.Query().Get("limit") == "1":
			_, _ = io.WriteString(w, `{"offset":0,"limit":1,"count":1,"totalCount":1,"data":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/network/integration/v1/sites" && r.URL.Query().Get("limit") == "200":
			_, _ = io.WriteString(w, `{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[{"id":"`+siteID+`","internalReference":"default","name":"Default"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/network/integration/v1/sites/"+siteID+"/traffic-matching-lists" && r.URL.Query().Get("limit") == "200":
			_, _ = io.WriteString(w, `{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[{"id":"`+listID+`","name":"Allowlist","type":"IPV4_ADDRESSES","items":[{"type":"IP_ADDRESS","value":"203.0.113.10"}]}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/network/integration/v1/sites/"+siteID+"/traffic-matching-lists/"+listID:
			_, _ = io.WriteString(w, `{"id":"`+listID+`","name":"Allowlist","type":"IPV4_ADDRESSES","items":[{"type":"IP_ADDRESS","value":"203.0.113.10"}]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxy/network/integration/v1/sites/"+siteID+"/traffic-matching-lists/"+listID:
			var err error
			updatedPayload, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := NewUniFiClient(ts.URL, "default", "key", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
	}
	c.httpClient = ts.Client()

	lists, err := c.ListNetworkLists()
	if err != nil {
		t.Fatalf("ListNetworkLists() error = %v", err)
	}
	if len(lists) != 1 || lists[0].ID != listID {
		t.Fatalf("ListNetworkLists() = %+v", lists)
	}

	nl, err := c.GetNetworkList(listID)
	if err != nil {
		t.Fatalf("GetNetworkList() error = %v", err)
	}
	nl.Items = append(nl.Items, TrafficMatchItem{Type: "SUBNET", Value: "198.51.100.0/24"})

	if err := c.UpdateNetworkList(nl); err != nil {
		t.Fatalf("UpdateNetworkList() error = %v", err)
	}
	if len(updatedPayload) == 0 {
		t.Fatal("expected PUT payload to be captured")
	}
	if !strings.Contains(string(updatedPayload), "198.51.100.0/24") {
		t.Fatalf("PUT payload = %s", string(updatedPayload))
	}
}

func TestUniFiClient_detectAPIBase_FallbackToLegacyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/network/integration/v1/sites":
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/integration/v1/sites":
			_, _ = io.WriteString(w, `{"offset":0,"limit":1,"count":0,"totalCount":0,"data":[]}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := NewUniFiClient(ts.URL, "default", "key", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
	}
	c.httpClient = ts.Client()

	base, err := c.detectAPIBase()
	if err != nil {
		t.Fatalf("detectAPIBase() error = %v", err)
	}
	if base != "/integration/v1" {
		t.Fatalf("detectAPIBase() = %q, want /integration/v1", base)
	}
}

func TestUniFiClient_ResolveSiteID_ByName(t *testing.T) {
	const siteID = "aaaa-bbbb-cccc"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/proxy/network/integration/v1/sites" && r.URL.Query().Get("limit") == "1":
			_, _ = io.WriteString(w, `{"offset":0,"limit":1,"count":1,"totalCount":1,"data":[]}`)
		case r.URL.Path == "/proxy/network/integration/v1/sites" && r.URL.Query().Get("limit") == "200":
			_, _ = io.WriteString(w, `{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[{"id":"`+siteID+`","internalReference":"home","name":"Home"}]}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := NewUniFiClient(ts.URL, "home", "key", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
	}
	c.httpClient = ts.Client()

	id, err := c.resolveSiteID()
	if err != nil {
		t.Fatalf("resolveSiteID() error = %v", err)
	}
	if id != siteID {
		t.Fatalf("resolveSiteID() = %q, want %q", id, siteID)
	}
}

func TestUniFiClient_ResolveSiteID_UUIDPassthrough(t *testing.T) {
	const siteUUID = "11111111-1111-1111-1111-111111111111"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("limit") == "1" {
			_, _ = io.WriteString(w, `{"offset":0,"limit":1,"count":0,"totalCount":0,"data":[]}`)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	c, err := NewUniFiClient(ts.URL, siteUUID, "key", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
	}
	c.httpClient = ts.Client()

	id, err := c.resolveSiteID()
	if err != nil {
		t.Fatalf("resolveSiteID() error = %v", err)
	}
	if id != siteUUID {
		t.Fatalf("resolveSiteID() = %q, want %q", id, siteUUID)
	}
}

func TestUniFiClient_UpdateNetworkList_PayloadShape(t *testing.T) {
	const (
		siteID = "11111111-1111-1111-1111-111111111111"
		listID = "list-1"
	)

	var captured []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Query().Get("limit") == "1":
			_, _ = io.WriteString(w, `{"offset":0,"limit":1,"count":1,"totalCount":1,"data":[]}`)
		case r.URL.Query().Get("limit") == "200":
			_, _ = io.WriteString(w, `{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[{"id":"`+siteID+`","internalReference":"default","name":"Default"}]}`)
		case r.Method == http.MethodPut:
			var err error
			captured, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, err := NewUniFiClient(ts.URL, "default", "key", false)
	if err != nil {
		t.Fatalf("NewUniFiClient() error = %v", err)
	}
	c.httpClient = ts.Client()
	c.siteID = siteID
	c.apiBase = "/proxy/network/integration/v1"

	nl := &NetworkList{
		Type:  "IPV4_ADDRESSES",
		ID:    listID,
		Name:  "Test",
		Items: []TrafficMatchItem{{Type: "IP_ADDRESS", Value: "1.2.3.4"}},
	}
	if err := c.UpdateNetworkList(nl); err != nil {
		t.Fatalf("UpdateNetworkList() error = %v", err)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decode captured payload: %v", err)
	}
	if _, ok := payload["items"]; !ok {
		t.Fatal("payload missing 'items'")
	}
	if _, ok := payload["type"]; !ok {
		t.Fatal("payload missing 'type'")
	}
}
