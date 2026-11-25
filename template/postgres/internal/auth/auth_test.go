package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/17xande-dev/password"
)

func TestTokenFormatAndDigest(t *testing.T) {
	token := NewToken()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token needs 32 base64url-encoded random bytes, got %d (%v)", len(raw), err)
	}
	if got := HashToken(token); got != TokenHash(sha256.Sum256([]byte(token))) {
		t.Fatal("digest is not SHA-256 of the bearer credential")
	}
}

func TestMain(m *testing.M) {
	p := password.DefaultParams
	p.Memory = 128
	p.Time = 1
	defer password.SetParamsForTesting(p)()
	m.Run()
}

func TestPasswordPolicyAndHash(t *testing.T) {
	if _, err := HashPassword(t.Context(), strings.Repeat("é", 11)); err == nil {
		t.Fatal("accepted 11 characters")
	}
	if err := ValidatePassword(strings.Repeat("a", 12) + "\xff"); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	plain := strings.Repeat("é", 12)
	hash, err := HashPassword(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, err := password.Check(t.Context(), hash, plain); !ok || rehash || err != nil {
		t.Fatalf("kit check: ok=%v rehash=%v error=%v", ok, rehash, err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail(" Sample <ACCOUNT@Example.com> ")
	if err != nil || got != "account@example.com" {
		t.Fatalf("normalized address = %q, error = %v", got, err)
	}
	if _, err := NormalizeEmail("not a mailbox"); err == nil {
		t.Fatal("accepted invalid address")
	}
}
