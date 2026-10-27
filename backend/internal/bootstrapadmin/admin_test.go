package bootstrapadmin

import (
	"strings"
	"testing"
)

func TestNewPasswordIsRandomURLSafeAndMeetsMinimumLength(t *testing.T) {
	first, err := newPassword()
	if err != nil {
		t.Fatalf("newPassword() error = %v", err)
	}
	second, err := newPassword()
	if err != nil {
		t.Fatalf("newPassword() second call error = %v", err)
	}

	if len(first) < 12 {
		t.Fatalf("generated password length = %d, want at least 12", len(first))
	}
	if first == second {
		t.Fatal("two generated passwords should not be equal")
	}
	if strings.ContainsAny(first, "+/= \r\n\t ") {
		t.Fatalf("generated password contains characters awkward to copy: %q", first)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, email := range []string{
		"admin@localhost.test",
		"owner@example.com",
	} {
		if err := validateEmail(email); err != nil {
			t.Errorf("validateEmail(%q) error = %v", email, err)
		}
	}

	for _, email := range []string{
		"",
		"not-an-email",
		"Admin User <admin@example.com>",
		"admin@example.com\r\nBcc: attacker@example.com",
	} {
		if err := validateEmail(email); err == nil {
			t.Errorf("validateEmail(%q) expected an error", email)
		}
	}
}
