package domain

import "time"

// StaffCredentials contains only the fields required to verify staff login.
type StaffCredentials struct {
	ID            int64
	DisplayName   string
	Email         string
	PasswordHash  string
	AccountStatus string
}

// StaffSession contains token digests intended for persistent storage.
type StaffSession struct {
	StaffID       int64
	TokenHash     [32]byte
	CSRFTokenHash [32]byte
	ExpiresAt     time.Time
}

// StaffSessionIdentity is the non-secret identity associated with a valid staff session.
// Roles contains current, active assignments; raw session and CSRF tokens are never included.
type StaffSessionIdentity struct {
	StaffID       int64
	DisplayName   string
	Email         string
	Roles         []string
	CSRFTokenHash [32]byte
	ExpiresAt     time.Time
}
