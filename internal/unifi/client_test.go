package unifi

import (
	"encoding/json"
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
