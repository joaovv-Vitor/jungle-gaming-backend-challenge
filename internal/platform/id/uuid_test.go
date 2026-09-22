package id

import "testing"

func TestNewProducesValidUniqueUUIDs(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("New() produced duplicate UUIDs")
	}
	if err := Validate(first); err != nil {
		t.Fatalf("Validate(New()) error = %v", err)
	}
}

func TestValidateRejectsNonCanonicalUUID(t *testing.T) {
	for _, value := range []string{"", "not-a-uuid", "550E8400-E29B-41D4-A716-446655440000", "550e8400e29b41d4a716446655440000"} {
		if err := Validate(value); err == nil {
			t.Fatalf("Validate(%q) error = nil", value)
		}
	}
}
