package repository

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound     = errors.New("запись не найдена")
	ErrConflict     = errors.New("конфликт уникальности")
	ErrInvalidInput = errors.New("некорректные входные данные")
	ErrInvalidState = errors.New("операция недопустима в текущем состоянии")
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
