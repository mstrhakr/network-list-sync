package syncer

import (
	"reflect"
	"testing"
	"time"

	"github.com/mstrhakr/network-list-sync/internal/store"
)

func TestMergeResolvedIPs_PreservesObservedSuperset(t *testing.T) {
	observed := map[string]string{
		"18.154.110.10": "integrations.ecimanufacturing.com",
		"18.238.25.33":  "integrations.ecimanufacturing.com",
	}
	current := map[string]string{
		"18.154.110.10": "integrations.ecimanufacturing.com",
		"18.238.25.43":  "integrations.ecimanufacturing.com",
	}

	got := mergeResolvedIPs(observed, current)
	want := map[string]string{
		"18.154.110.10": "integrations.ecimanufacturing.com",
		"18.238.25.33":  "integrations.ecimanufacturing.com",
		"18.238.25.43":  "integrations.ecimanufacturing.com",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeResolvedIPs = %v, want %v", got, want)
	}
}

func TestObservedIPRetention_UsesJobSettingOrDefault(t *testing.T) {
	if got := observedIPRetention(nil); got != time.Duration(store.DefaultObservedIPTTLHours)*time.Hour {
		t.Fatalf("observedIPRetention(nil) = %v", got)
	}

	job := &store.SyncJob{ObservedIPTTLHours: 12}
	if got := observedIPRetention(job); got != 12*time.Hour {
		t.Fatalf("observedIPRetention(job) = %v, want %v", got, 12*time.Hour)
	}

	disabled := &store.SyncJob{ObservedIPTTLHours: 0}
	if got := observedIPRetention(disabled); got != 0 {
		t.Fatalf("observedIPRetention(disabled) = %v, want 0", got)
	}
}
