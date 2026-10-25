package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	password := "correct horse battery staple"
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("encoded hash contains the plaintext password")
	}
	ok, err := VerifyPassword(password, encoded)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct password) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = VerifyPassword("a different password", encoded)
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong password) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestHashPasswordRejectsShortPassword(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrInvalidPassword {
		t.Fatalf("HashPassword(short) error = %v, want ErrInvalidPassword", err)
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{"", "bcrypt$not-a-hash", "pbkdf2-sha256$600000$bad$bad"} {
		if _, err := VerifyPassword("some password", encoded); err == nil {
			t.Errorf("VerifyPassword(%q) expected an error", encoded)
		}
	}
}

func TestSessionTokensAreOpaqueAndDifferent(t *testing.T) {
	first, firstDigest, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() error = %v", err)
	}
	second, secondDigest, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() second error = %v", err)
	}
	if first == second {
		t.Fatal("two session tokens unexpectedly match")
	}
	if firstDigest == secondDigest {
		t.Fatal("two session token digests unexpectedly match")
	}
	if got := HashToken(first); got != firstDigest {
		t.Fatal("HashToken(token) does not match generated digest")
	}
}
