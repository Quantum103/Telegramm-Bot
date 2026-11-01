package domain

import "errors"

var (
	ErrNotFound           = errors.New("ресурс не найден")
	ErrConflict           = errors.New("конфликт состояния или уникальности")
	ErrInvalidInput       = errors.New("некорректные входные данные")
	ErrInvalidState       = errors.New("операция недопустима в текущем состоянии")
	ErrNoPreviousQuestion = errors.New("предыдущего вопроса нет")
	ErrNoNextQuestion     = errors.New("следующего вопроса нет")
	ErrActiveCourse       = errors.New("у ученика уже есть незавершённый курс")
	ErrNoCurator          = errors.New("для прохождения курса не назначен активный куратор")
	ErrLegacyProgress     = errors.New("найдено старое незавершённое прохождение, которое нельзя однозначно связать с курсом")
)
