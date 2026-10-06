package store

import (
	"errors"
	"testing"
)

func TestSourceValidationErrorsAreTyped(t *testing.T) {
	s := sourceTestStore(t)
	leaf := createTestSource(t, s, "leaf", "192.0.2.1")
	parent := createTestSource(t, s, "parent", "", leaf.ID)
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"empty content", func() error { return s.ValidateSourceEntries("", nil) }},
		{"missing reference", func() error { return s.ValidateSourceEntries("", []int64{99999}) }},
		{"missing name", func() error {
			_, err := s.CreateSourceList(&SourceList{Hostnames: "192.0.2.1"})
			return err
		}},
		{"wrapped cycle", func() error {
			leaf.IncludedListIDs = []int64{parent.ID}
			return s.UpdateSourceList(leaf)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var validationErr *SourceValidationError
			if err := tc.fn(); !errors.As(err, &validationErr) {
				t.Fatalf("error=%v, want SourceValidationError", err)
			}
		})
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var validationErr *SourceValidationError
	err := s.ValidateSourceEntries("192.0.2.1", nil)
	if err == nil || errors.As(err, &validationErr) {
		t.Fatalf("storage error=%v, must not be SourceValidationError", err)
	}
}
