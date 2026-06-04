package syncer

import (
	"reflect"
	"testing"
)

func TestNewMultiResolverWithServers_ValidationAndDedup(t *testing.T) {
	if _, err := newMultiResolverWithServers(nil); err == nil {
		t.Fatal("expected error for empty servers")
	}

	r, err := newMultiResolverWithServers([]string{"1.1.1.1:53", "", "1.1.1.1:53", "8.8.8.8"})
	if err != nil {
		t.Fatalf("newMultiResolverWithServers() error = %v", err)
	}
	want := []string{"1.1.1.1:53", "8.8.8.8"}
	if !reflect.DeepEqual(r.publicServers, want) {
		t.Fatalf("publicServers = %v, want %v", r.publicServers, want)
	}
}

func TestResolverHelpers(t *testing.T) {
	if got := ensureDNSServerPort("8.8.8.8"); got != "8.8.8.8:53" {
		t.Fatalf("ensureDNSServerPort = %q", got)
	}
	if got := ensureDNSServerPort("8.8.8.8:5353"); got != "8.8.8.8:5353" {
		t.Fatalf("ensureDNSServerPort kept port = %q", got)
	}

	result := map[string]string{}
	addResolvedSource(result, "203.0.113.10", "example.com")
	addResolvedSource(result, "203.0.113.10", "example.com")
	addResolvedSource(result, "203.0.113.10", "example.net")
	if got := result["203.0.113.10"]; got != "example.com, example.net" {
		t.Fatalf("addResolvedSource result = %q", got)
	}

	if got := uniqueStrings([]string{"a", "", "a", "b", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("uniqueStrings = %v", got)
	}

	if got := normalizeHostnameLine("  host.example.com # comment "); got != "host.example.com" {
		t.Fatalf("normalizeHostnameLine = %q", got)
	}
	if got, ok := normalizeIPv4Literal("203.0.113.10"); !ok || got != "203.0.113.10" {
		t.Fatalf("normalizeIPv4Literal = %q ok=%v", got, ok)
	}
	if got, ok := normalizeIPv4CIDR("203.0.113.0/24"); !ok || got != "203.0.113.0/24" {
		t.Fatalf("normalizeIPv4CIDR = %q ok=%v", got, ok)
	}
	if got := normalizeDNSName(" Example.COM. "); got != "example.com" {
		t.Fatalf("normalizeDNSName = %q", got)
	}

	if got := bytesCompare([]byte{1, 2}, []byte{1, 2, 3}); got != -1 {
		t.Fatalf("bytesCompare short<long = %d", got)
	}
	if got := bytesCompare([]byte{3}, []byte{2}); got != 1 {
		t.Fatalf("bytesCompare 3>2 = %d", got)
	}
	if got := bytesCompare([]byte{1, 2}, []byte{1, 2}); got != 0 {
		t.Fatalf("bytesCompare equal = %d", got)
	}
}
