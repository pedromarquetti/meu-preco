package auth

import (
	"testing"
	"time"

	"meupreco/internal/domain"
)

const tokenSecrets = "a-very-long-unit-test-secret-for-hs256-signing"

func user(id int64) *domain.User {
	return &domain.User{ID: id, Email: "a@example.com", Role: "user"}
}

func newTestService(accessTTL, refreshTTL time.Duration) *TokenService {
	return NewTokenService(tokenSecrets, accessTTL, refreshTTL)
}

func TestIssueAndValidateAccess(t *testing.T) {
	svc := newTestService(15*time.Minute, 24*time.Hour)
	tok, err := svc.IssueAccess(user(7))
	if err != nil {
		t.Fatal(err)
	}

	claims, err := svc.Validate(tok, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Type != TypeAccess || claims.Role != "user" || claims.Email != "a@example.com" {
		t.Errorf("claims = %+v", claims)
	}
	if id, err := SubjectID(claims); err != nil || id != 7 {
		t.Errorf("subject = %d, %v", id, err)
	}
}

func TestIssueRefreshCarriesFamily(t *testing.T) {
	svc := newTestService(15*time.Minute, 24*time.Hour)
	tok, err := svc.IssueRefresh(user(7), "family-x")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.Validate(tok, TypeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Family != "family-x" {
		t.Errorf("family = %q", claims.Family)
	}
	if claims.ID == "" {
		t.Error("refresh token missing jti")
	}
}

func TestIssueRefreshGeneratesFamilyWhenEmpty(t *testing.T) {
	svc := newTestService(time.Minute, time.Hour)
	a, _ := svc.IssueRefresh(user(1), "")
	b, _ := svc.IssueRefresh(user(1), "")
	if a == b {
		t.Error("two fresh refreshes must differ")
	}
	ca, _ := svc.Validate(a, TypeRefresh)
	cb, _ := svc.Validate(b, TypeRefresh)
	if ca.Family == "" || ca.Family == cb.Family {
		t.Errorf("families must be distinct and non-empty: %q / %q", ca.Family, cb.Family)
	}
}

func TestValidateRejectsWrongType(t *testing.T) {
	svc := newTestService(time.Minute, time.Hour)
	access, _ := svc.IssueAccess(user(1))
	if _, err := svc.Validate(access, TypeRefresh); err == nil {
		t.Error("access token accepted as refresh")
	}
	refresh, _ := svc.IssueRefresh(user(1), "f")
	if _, err := svc.Validate(refresh, TypeAccess); err == nil {
		t.Error("refresh token accepted as access")
	}
}

func TestValidateRejectsExpired(t *testing.T) {
	svc := newTestService(time.Millisecond, time.Hour)
	tok, _ := svc.IssueAccess(user(1))
	time.Sleep(5 * time.Millisecond)
	if _, err := svc.Validate(tok, TypeAccess); err == nil {
		t.Error("expired token accepted")
	}
}

func TestValidateRejectsTampering(t *testing.T) {
	svc := newTestService(time.Minute, time.Hour)
	tok, _ := svc.IssueAccess(user(1))
	last := tok[len(tok)-1]
	alt := byte('a')
	if last == 'a' {
		alt = 'b'
	}
	tampered := tok[:len(tok)-1] + string(alt)
	if _, err := svc.Validate(tampered, TypeAccess); err == nil {
		t.Error("tampered token accepted")
	}
}

func TestValidateRejectsForeignSignature(t *testing.T) {
	svc := newTestService(time.Minute, time.Hour)
	other := NewTokenService("a-different-secret-with-at-least-32-chars-xyz", time.Minute, time.Hour)
	tok, _ := other.IssueAccess(user(1))
	if _, err := svc.Validate(tok, TypeAccess); err == nil {
		t.Error("token signed by another service accepted")
	}
}

func TestRandomHex(t *testing.T) {
	a, err := RandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RandomHex(16)
	if a == b || len(a) != 32 {
		t.Errorf("RandomHex yielded %q / %q", a, b)
	}
}

func TestTokenHashDeterministicAndOpaque(t *testing.T) {
	tok := "some.jwt.value"
	h1 := TokenHash(tok)
	h2 := TokenHash(tok)
	if h1 != h2 || h1 == tok {
		t.Errorf("TokenHash = %q", h1)
	}
}
