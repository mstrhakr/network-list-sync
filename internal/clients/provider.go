package clients

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mstrhakr/network-list-sync/internal/store"
)

// ProviderType identifies the backend provider.
type ProviderType string

const (
	ProviderUniFi ProviderType = "unifi"
	ProviderNPM   ProviderType = "npm"
)

// TrafficMatchItem represents a single entry in a target list.
// For IP lists: Type is IP_ADDRESS, SUBNET, or IP_ADDRESS_RANGE.
// For port lists: Type is PORT_NUMBER or PORT_NUMBER_RANGE (UniFi only).
type TrafficMatchItem struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
	Start string `json:"start,omitempty"`
	Stop  string `json:"stop,omitempty"`
}

// UnmarshalJSON accepts both string and numeric scalar fields for value/start/stop.
func (t *TrafficMatchItem) UnmarshalJSON(data []byte) error {
	type rawItem struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
		Start json.RawMessage `json:"start"`
		Stop  json.RawMessage `json:"stop"`
	}
	var raw rawItem
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	t.Type = raw.Type
	t.Value, err = scalarToString(raw.Value)
	if err != nil {
		return fmt.Errorf("decode item.value: %w", err)
	}
	t.Start, err = scalarToString(raw.Start)
	if err != nil {
		return fmt.Errorf("decode item.start: %w", err)
	}
	t.Stop, err = scalarToString(raw.Stop)
	if err != nil {
		return fmt.Errorf("decode item.stop: %w", err)
	}
	return nil
}

func scalarToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s, nil
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		return n.String(), nil
	}
	var b bool
	if err := json.Unmarshal(trimmed, &b); err == nil {
		if b {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("unsupported scalar type: %s", string(trimmed))
}

// NetworkList is the common representation of a provider-managed IP/CIDR list.
// SatisfyAny and PassAuth are NPM-specific fields preserved for round-trip updates
// and informational display; they are ignored by the UniFi provider.
type NetworkList struct {
	Type       string             `json:"type"`
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Items      []TrafficMatchItem `json:"items,omitempty"`
	SatisfyAny bool               `json:"satisfy_any,omitempty"`
	PassAuth   bool               `json:"pass_auth,omitempty"`
}

// Provider is the interface all provider adapters must implement.
type Provider interface {
	ListNetworkLists() ([]NetworkList, error)
	GetNetworkList(listID string) (*NetworkList, error)
	UpdateNetworkList(nl *NetworkList) error
}

// New creates the appropriate provider adapter for the given store.Controller.
func New(ctrl *store.Controller) (Provider, error) {
	switch ProviderType(strings.ToLower(strings.TrimSpace(ctrl.Provider))) {
	case ProviderNPM:
		return NewNPMClient(ctrl.URL, ctrl.Site, ctrl.APIKey, ctrl.SkipTLSVerify)
	default:
		return NewUniFiClient(ctrl.URL, ctrl.Site, ctrl.APIKey, ctrl.SkipTLSVerify)
	}
}
