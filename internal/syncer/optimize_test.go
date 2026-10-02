package syncer

import (
	"reflect"
	"strconv"
	"testing"
)

func TestOptimizeIPv4Entries_DeduplicatesAndRemovesCoveredEntries(t *testing.T) {
	input := []string{
		"192.168.0.0/24",
		"192.168.0.5",
		"192.168.0.5",
		"192.168.1.5",
		"192.168.1.5/32",
	}

	got, stats, err := OptimizeIPv4Entries(input, false)
	if err != nil {
		t.Fatalf("OptimizeIPv4Entries() error = %v", err)
	}
	want := []string{"192.168.0.0/24", "192.168.1.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OptimizeIPv4Entries() = %v, want %v", got, want)
	}
	if stats.DuplicateEntries != 2 || stats.CoveredEntriesRemoved != 1 || stats.EntriesSaved != 3 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestOptimizeIPv4Entries_CollapsesFull24ButNotPartial24(t *testing.T) {
	input := make([]string, 0, 257)
	for i := 0; i < 256; i++ {
		input = append(input, "192.168.0."+strconv.Itoa(i))
	}
	input = append(input, "192.168.1.50")

	got, stats, err := OptimizeIPv4Entries(input, true)
	if err != nil {
		t.Fatalf("OptimizeIPv4Entries() error = %v", err)
	}
	want := []string{"192.168.0.0/24", "192.168.1.50"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OptimizeIPv4Entries() = %v, want %v", got, want)
	}
	if stats.CIDRBlocks != 1 || stats.OutputEntries != 2 || stats.EntriesSaved != 255 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestOptimizeIPv4Entries_OnlyCollapsesExactCoverageAndMinimumPrefix(t *testing.T) {
	input := []string{"10.0.0.0/23", "10.0.2.0/25", "10.0.2.128", "10.0.2.130"}
	got, stats, err := OptimizeIPv4Entries(input, true)
	if err != nil {
		t.Fatalf("OptimizeIPv4Entries() error = %v", err)
	}
	want := []string{"10.0.0.0/23", "10.0.2.0/25", "10.0.2.128", "10.0.2.130"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OptimizeIPv4Entries() = %v, want %v", got, want)
	}
	if stats.CIDRBlocks != 2 || stats.CoveredEntriesRemoved != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestOptimizeIPv4Entries_RejectsNonIPv4Entries(t *testing.T) {
	for _, input := range [][]string{{"example.com"}, {"2001:db8::1"}, {"192.0.2.0/33"}} {
		if _, _, err := OptimizeIPv4Entries(input, true); err == nil {
			t.Errorf("OptimizeIPv4Entries(%q) expected error", input)
		}
	}
}