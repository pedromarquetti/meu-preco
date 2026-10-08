package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost matches bcrypt.DefaultCost (10): strong enough for account
// credentials, slow enough to frustrate offline cracking of a leaked dump.
const bcryptCost = bcrypt.DefaultCost

var (
	// ErrPasswordTooShort flags passwords below the minimum length.
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
)

// HashPassword hashes a plaintext password with bcrypt. The hash is what gets stored
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(b), err
}

// CheckPassword reports whether the plaintext matches a bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ValidatePassword enforces the registration password policy.
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return ErrPasswordTooShort
	}
	return nil
}
