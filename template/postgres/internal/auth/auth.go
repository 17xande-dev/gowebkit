// Package auth holds account and credential types and application policy.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/17xande-dev/password"
)

// Role controls access to administrative actions.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// User is a local account and its sign-in state.
type User struct {
	ID                 int64
	Name               string
	Email              string
	Role               Role
	PasswordHash       string
	Disabled           bool
	MustChangePassword bool
}

// Session holds the times used to judge a stored session against the policy.
type Session struct {
	UserID    int64
	CreatedAt time.Time
	LastSeen  time.Time
}

// SetupState records a one-time token; a consumed token cannot be reissued.
type SetupState struct {
	TokenHash TokenHash
	Consumed  bool
}

// TokenHash is the SHA-256 digest persisted instead of a bearer credential.
type TokenHash [sha256.Size]byte

// NewToken returns a cookie-safe encoding of 32 random bytes.
func NewToken() string {
	var random [32]byte
	rand.Read(random[:]) // crypto/rand.Read always fills the buffer or terminates the process.
	return base64.RawURLEncoding.EncodeToString(random[:])
}

// HashToken hashes the exact credential string without normalizing it.
func HashToken(token string) TokenHash { return sha256.Sum256([]byte(token)) }

// NormalizeEmail validates a mailbox and keeps its canonical account key.
func NormalizeEmail(input string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(input))
	if err != nil {
		return "", errors.New("email must be a valid mailbox")
	}
	return strings.ToLower(address.Address), nil
}

// ValidatePassword enforces the same length policy for every password write.
func ValidatePassword(plaintext string) error {
	if !utf8.ValidString(plaintext) || utf8.RuneCountInString(plaintext) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	return nil
}

// HashPassword checks the application's policy before calling the password kit.
func HashPassword(ctx context.Context, plaintext string) (string, error) {
	if err := ValidatePassword(plaintext); err != nil {
		return "", err
	}
	return password.Hash(ctx, plaintext)
}
