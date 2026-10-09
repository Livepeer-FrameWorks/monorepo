package auth

import (
	"golang.org/x/crypto/bcrypt"
)

// DummyPasswordHash is a bcrypt hash, at the bcrypt.DefaultCost HashPassword
// stores, of a random value nobody kept. A login with no stored hash to check
// compares against it so the answer costs the same bcrypt work as a wrong
// password; the caller rejects the login whatever the comparison returns.
const DummyPasswordHash = "$2a$10$Z8J5dOEq3Z4f8hh9jpCEQ.IXz8kih2smzS8RaYSABZ/Y346iZ0t5q"

// HashPassword hashes a password using bcrypt
func HashPassword(password string, cost ...int) (string, error) {
	bcryptCost := bcrypt.DefaultCost
	if len(cost) > 0 {
		bcryptCost = cost[0]
	}

	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(bytes), err
}

// CheckPassword compares a password with its hash
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
