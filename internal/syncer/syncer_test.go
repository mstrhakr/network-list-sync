package syncer

import (
	"reflect"
	"testing"
	"time"

	"github.com/mstrhakr/network-list-sync/internal/clients"
	"github.com/mstrhakr/network-list-sync/internal/store"
)

type testNetworkListProvider struct {
	list    *clients.NetworkList
	updated bool
}

func (p *testNetworkListProvider) ListNetworkLists() ([]clients.NetworkList, error) {
	return []clients.NetworkList{*p.list}, nil
}

func (p *testNetworkListProvider) GetNetworkList(string) (*clients.NetworkList, error) {
	copy := *p.list
	copy.Items = append([]clients.TrafficMatchItem(nil), p.list.Items...)
	return &copy, nil
}

func (p *testNetworkListProvider) UpdateNetworkList(list *clients.NetworkList) error {
	p.updated = true
	p.list = list
	return nil
}

func TestSyncTargetWritesMixedIPAndCIDRItems(t *testing.T) {
	provider := &testNetworkListProvider{list: &clients.NetworkList{
		ID:    "list-1",
		Name:  "threat-feed",
		Type:  "IPV4_ADDRESSES",
		Items: []clients.TrafficMatchItem{{Type: "IP_ADDRESS", Value: "192.0.2.1"}, {Type: "IP_ADDRESS", Value: "192.0.2.1"}},
	}}

	result, err := New().syncTarget(provider, "list-1", []string{"192.0.2.0/24", "198.51.100.10"}, nil)
	if err != nil {
		t.Fatalf("syncTarget() error = %v", err)
	}
	if !provider.updated {
		t.Fatal("syncTarget() did not replace duplicate/old entries")
	}
	if len(result.items) != 2 || result.items[0].Type != "SUBNET" || result.items[1].Type != "IP_ADDRESS" {
		t.Fatalf("sent items = %+v, want mixed subnet and individual IP", result.items)
	}
}

func TestSyncTargetRemovesDuplicateItemsFromExistingList(t *testing.T) {
	provider := &testNetworkListProvider{list: &clients.NetworkList{
		ID:   "list-1",
		Name: "threat-feed",
		Type: "IPV4_ADDRESSES",
		Items: []clients.TrafficMatchItem{
			{Type: "IP_ADDRESS", Value: "192.0.2.1"},
			{Type: "IP_ADDRESS", Value: "192.0.2.1"},
		},
	}}

	result, err := New().syncTarget(provider, "list-1", []string{"192.0.2.1"}, map[string]string{"192.0.2.1": "feed"})
	if err != nil {
		t.Fatalf("syncTarget() error = %v", err)
	}
	if !provider.updated || result.changes != 1 {
		t.Fatalf("representation cleanup: updated=%v changes=%d", provider.updated, result.changes)
	}
	if len(result.items) != 1 || result.items[0].Value != "192.0.2.1" {
		t.Fatalf("sent items = %+v, want one deduplicated IP", result.items)
	}
}

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
