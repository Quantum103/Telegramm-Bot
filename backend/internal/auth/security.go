// Package auth contains security primitives for staff authentication.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

const (
	passwordAlgorithm  = "pbkdf2-sha256"
	passwordIterations = 600_000
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	maxPasswordBytes   = 1024
	sessionTokenBytes  = 32
)

var (
	ErrInvalidPassword = errors.New("password does not meet requirements")
	ErrInvalidHash     = errors.New("invalid password hash")
)

// HashPassword creates a salted PBKDF2-HMAC-SHA256 hash suitable for storage.
// Passwords must be 12–1024 bytes; callers must never log the password or hash.
func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > maxPasswordBytes {
		return "", ErrInvalidPassword
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key, err := pbkdf2.Key([]byte(password), salt, passwordIterations, passwordKeyBytes, sha256.New)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", passwordAlgorithm, passwordIterations,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword compares a candidate against a stored HashPassword value.
// Malformed or unsupported hashes fail closed.
func VerifyPassword(password, encoded string) (bool, error) {
	if len(password) > maxPasswordBytes {
		return false, ErrInvalidPassword
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordAlgorithm {
		return false, ErrInvalidHash
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations != passwordIterations {
		return false, ErrInvalidHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil || len(salt) != passwordSaltBytes {
		return false, ErrInvalidHash
	}
	expected, err := enc.DecodeString(parts[3])
	if err != nil || len(expected) != passwordKeyBytes {
		return false, ErrInvalidHash
	}
	actual, err := deriveKey([]byte(password), salt, iterations, len(expected), sha256.New)
	if err != nil {
		return false, fmt.Errorf("derive candidate password hash: %w", err)
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

// NewSessionToken returns a cryptographically random opaque token and its SHA-256 digest.
// Store only the digest in the database; send the raw token only to the client cookie.
func NewSessionToken() (token string, digest [32]byte, err error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", digest, fmt.Errorf("generate session token: %w", err)
	}
	digest = sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), digest, nil
}

// HashToken hashes an opaque token before database lookup.
func HashToken(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

func deriveKey(password, salt []byte, iterations, length int, h func() hash.Hash) ([]byte, error) {
	return pbkdf2.Key(password, salt, iterations, length, h)
}
