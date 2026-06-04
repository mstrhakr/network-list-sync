package npm

import (
	"testing"
)

func TestNormalizeControllerBaseURL_DefaultsToAPI(t *testing.T) {
	base, api, err := normalizeControllerBaseURL("http://192.168.1.171:81")
	if err != nil {
		t.Fatalf("normalizeControllerBaseURL() error = %v", err)
	}
	if base != "http://192.168.1.171:81" {
		t.Fatalf("base = %q", base)
	}
	if api != "/api" {
		t.Fatalf("api = %q, want /api", api)
	}
}

func TestNormalizeControllerBaseURL_AcceptsExplicitAPIPath(t *testing.T) {
	base, api, err := normalizeControllerBaseURL("http://192.168.1.171:81/api")
	if err != nil {
		t.Fatalf("normalizeControllerBaseURL() error = %v", err)
	}
	if base != "http://192.168.1.171:81" || api != "/api" {
		t.Fatalf("got base=%q api=%q", base, api)
	}
}
