package syncer

import (
	"reflect"
	"strings"
	"testing"
)

func TestSubtractIPv4EntriesPreservesCIDRHole(t *testing.T) {
	got, stats, err := SubtractIPv4Entries(
		map[string]string{"10.0.0.0/24": "include.example"},
		map[string]string{"10.0.0.64/26": "exclude.example"},
	)
	if err != nil {
		t.Fatalf("SubtractIPv4Entries() error = %v", err)
	}
	want := map[string]string{
		"10.0.0.0/26":   "include.example",
		"10.0.0.128/25": "include.example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubtractIPv4Entries() = %v, want %v", got, want)
	}
	if stats.InputEntries != 1 || stats.IncludedEntriesAffected != 1 || stats.OutputEntries != 2 {
		t.Fatalf("unexpected exclusion stats: %+v", stats)
	}
}

func TestSubtractIPv4EntriesAggregatesExclusionsAndKeepsHostEntries(t *testing.T) {
	got, stats, err := SubtractIPv4Entries(
		map[string]string{"192.0.2.0/24": "include.example", "192.0.2.10": "host.example"},
		map[string]string{"192.0.2.0/25": "exclude-a", "192.0.2.64/26": "exclude-b"},
	)
	if err != nil {
		t.Fatalf("SubtractIPv4Entries() error = %v", err)
	}
	want := map[string]string{"192.0.2.128/25": "include.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubtractIPv4Entries() = %v, want %v", got, want)
	}
	if stats.InputEntries != 2 || stats.IncludedEntriesAffected != 2 || stats.OutputEntries != 1 {
		t.Fatalf("unexpected exclusion stats: %+v", stats)
	}
}

func TestSubtractIPv4EntriesAllowsEmptySetAndLeavesDisjointEntries(t *testing.T) {
	got, stats, err := SubtractIPv4Entries(
		map[string]string{"192.0.2.1": "one", "198.51.100.0/24": "two"},
		map[string]string{"192.0.2.1/32": "exclude"},
	)
	if err != nil {
		t.Fatalf("SubtractIPv4Entries() error = %v", err)
	}
	want := map[string]string{"198.51.100.0/24": "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubtractIPv4Entries() = %v, want %v", got, want)
	}
	if stats.IncludedEntriesAffected != 1 || stats.OutputEntries != 1 {
		t.Fatalf("unexpected exclusion stats: %+v", stats)
	}

	got, stats, err = SubtractIPv4Entries(
		map[string]string{"203.0.113.0/24": "include"},
		map[string]string{"203.0.113.0/24": "exclude"},
	)
	if err != nil || len(got) != 0 || stats.OutputEntries != 0 {
		t.Fatalf("full exclusion = %v, %+v, %v; want empty output", got, stats, err)
	}
}

func TestSubtractIPv4EntriesRejectsNonIPv4Values(t *testing.T) {
	for _, testCase := range []struct {
		includes   map[string]string
		exclusions map[string]string
	}{
		{includes: map[string]string{"2001:db8::1": "include"}},
		{includes: map[string]string{"192.0.2.1": "include"}, exclusions: map[string]string{"2001:db8::1": "exclude"}},
	} {
		if _, _, err := SubtractIPv4Entries(testCase.includes, testCase.exclusions); err == nil {
			t.Errorf("SubtractIPv4Entries(%v, %v) expected error", testCase.includes, testCase.exclusions)
		}
	}
}

func TestSubtractIPv4EntriesEnforcesComparisonBudget(t *testing.T) {
	_, _, err := subtractIPv4Entries(
		map[string]string{"192.0.2.1": "one", "198.51.100.1": "two"},
		map[string]string{"203.0.113.1": "exclude"},
		10, 1,
	)
	if err == nil {
		t.Fatal("SubtractIPv4Entries() exceeded comparison budget without error")
	}
}

func TestFormatExclusionDetailsShowsCountsAndEntries(t *testing.T) {
	details := formatExclusionDetails(map[string]string{
		"192.0.2.1":    "single.example",
		"192.0.2.0/24": "range.example",
	}, ExclusionStats{InputEntries: 2, IncludedEntriesAffected: 1, OutputEntries: 3})
	for _, expected := range []string{
		"2 resolved entries", "included entries affected: 1", "3 entries remain before optimization",
		"192.0.2.0/24 (range.example)", "192.0.2.1 (single.example)",
	} {
		if !strings.Contains(details, expected) {
			t.Errorf("formatExclusionDetails() = %q; missing %q", details, expected)
		}
	}
}
