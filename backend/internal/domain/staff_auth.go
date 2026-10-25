package domain

import "time"

// StaffCredentials содержит минимальные данные для проверки входа сотрудника.
type StaffCredentials struct {
	ID           int64
	DisplayName  string
	Email        string
	PasswordHash string
	AccountStatus string
}

// StaffSession содержит только хеши секретов, предназначенные для хранения в БД.
type StaffSession struct {
	StaffID       int64
	TokenHash     [32]byte
	CSRFTokenHash [32]byte
	ExpiresAt     time.Time
}
