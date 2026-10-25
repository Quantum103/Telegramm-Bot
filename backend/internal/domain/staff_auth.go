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
