package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The dummy hash must cost what a stored hash costs, or an unknown email still
// answers faster than a wrong password.
func TestDummyPasswordHashMatchesStoredHashCost(t *testing.T) {
	dummyCost, err := bcrypt.Cost([]byte(DummyPasswordHash))
	if err != nil {
		t.Fatalf("dummy hash is not a bcrypt hash: %v", err)
	}
	stored, err := HashPassword("any-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	storedCost, err := bcrypt.Cost([]byte(stored))
	if err != nil {
		t.Fatalf("stored hash cost: %v", err)
	}
	if dummyCost != storedCost {
		t.Fatalf("dummy hash cost %d, stored hash cost %d", dummyCost, storedCost)
	}
}
