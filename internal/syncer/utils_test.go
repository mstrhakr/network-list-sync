package syncer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mstrhakr/network-list-sync/internal/clients"
)

func TestNew_ReturnsSyncer(t *testing.T) {
	if New() == nil {
		t.Fatal("New() returned nil")
	}
}

func TestDiffFormatAndItemConversion(t *testing.T) {
	oldIPs := []string{"10.0.0.1", "10.0.0.2"}
	newIPs := []string{"10.0.0.2", "10.0.0.3", "10.0.0.0/24"}
	added, removed, kept := DiffIPs(oldIPs, newIPs)

	if !reflect.DeepEqual(added, []string{"10.0.0.3", "10.0.0.0/24"}) {
		t.Fatalf("added = %v", added)
	}
	if !reflect.DeepEqual(removed, []string{"10.0.0.1"}) {
		t.Fatalf("removed = %v", removed)
	}
	if !reflect.DeepEqual(kept, []string{"10.0.0.2"}) {
		t.Fatalf("kept = %v", kept)
	}

	hostSources := map[string]string{
		"10.0.0.3":    "example.com",
		"10.0.0.2":    "example.com",
		"10.0.0.0/24": "10.0.0.0/24",
	}
	diff := FormatDiff(added, removed, kept, hostSources)
	if !strings.Contains(diff, "+ 10.0.0.3 (example.com)") {
		t.Fatalf("diff missing added entry: %q", diff)
	}
	if !strings.Contains(diff, "- 10.0.0.1 (unknown)") {
		t.Fatalf("diff missing unknown removed entry: %q", diff)
	}
	if !strings.Contains(diff, "  10.0.0.2 (example.com)") {
		t.Fatalf("diff missing kept entry: %q", diff)
	}

	items := IPsToItems(newIPs)
	if items[0].Type != "IP_ADDRESS" || items[2].Type != "SUBNET" {
		t.Fatalf("IPsToItems types = %+v", items)
	}

	extraItems := append(items, clients.TrafficMatchItem{Type: "PORT_NUMBER", Value: "443"})
	extracted := ExtractIPsFromItems(extraItems)
	if !reflect.DeepEqual(extracted, newIPs) {
		t.Fatalf("ExtractIPsFromItems = %v, want %v", extracted, newIPs)
	}
}

func TestSortedIPs_StableOrdering(t *testing.T) {
	input := map[string]string{
		"10.0.0.1":    "a",
		"10.0.0.0/24": "b",
		"9.0.0.1":     "c",
		"zzz":         "d",
	}
	got := SortedIPs(input)
	want := []string{"9.0.0.1", "10.0.0.0/24", "10.0.0.1", "zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedIPs = %v, want %v", got, want)
	}
}
