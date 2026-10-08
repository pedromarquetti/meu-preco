package auth

import (
	"strings"
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("password stored in plaintext")
	}
	if strings.Contains(hash, "correct") {
		t.Fatal("hash leaks part of the plaintext")
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Error("valid password rejected")
	}
	if CheckPassword(hash, "wrong guess") {
		t.Error("wrong password accepted")
	}
}

func TestHashPasswordUniqueSalt(t *testing.T) {
	a, _ := HashPassword("same-password")
	b, _ := HashPassword("same-password")
	if a == b {
		t.Error("two hashes of the same password must differ (salting)")
	}
}

func TestValidatePassword(t *testing.T) {
	for short := range []int{0, 1, 7} {
		pw := strings.Repeat("x", short)
		if err := ValidatePassword(pw); err == nil {
			t.Errorf("password %q accepted", pw)
		}
	}
	if err := ValidatePassword(strings.Repeat("x", 8)); err != nil {
		t.Errorf("8-char password rejected: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("x", 72)); err != nil {
		t.Errorf("72-char password rejected: %v", err)
	}
}
