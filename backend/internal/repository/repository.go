package repository

import (
	"errors"
	"fmt"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound             = domain.ErrNotFound
	ErrConflict             = domain.ErrConflict
	ErrInvalidInput         = domain.ErrInvalidInput
	ErrInvalidState         = domain.ErrInvalidState
	ErrNoPreviousQuestion   = domain.ErrNoPreviousQuestion
	ErrNoNextQuestion       = domain.ErrNoNextQuestion
)

func mapDatabaseError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%s: %w", operation, ErrConflict)
		case "23503", "23514", "22P02":
			return fmt.Errorf("%s: %w", operation, ErrInvalidInput)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
