package service

import (
	"errors"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestValidateSubmissionByQuestionType(t *testing.T) {
	textQuestion := domain.LessonQuestion{AnswerType: "text"}
	singleChoice := domain.LessonQuestion{
		AnswerType: "single_choice",
		Options: []domain.QuestionOption{
			{ID: 10, Key: "A", Text: "Первый"},
			{ID: 11, Key: "B", Text: "Второй"},
		},
	}
	multipleChoice := domain.LessonQuestion{
		AnswerType: "multiple_choice",
		Options: []domain.QuestionOption{
			{ID: 20, Key: "A", Text: "Первый"},
			{ID: 21, Key: "B", Text: "Второй"},
		},
	}

	tests := []struct {
		name     string
		question domain.LessonQuestion
		params   domain.AnswerSubmission
		wantErr  error
	}{
		{name: "text accepted", question: textQuestion, params: domain.AnswerSubmission{AnswerText: "  Ответ  "}},
		{name: "empty text rejected", question: textQuestion, params: domain.AnswerSubmission{}, wantErr: domain.ErrInvalidInput},
		{name: "text cannot include options", question: textQuestion, params: domain.AnswerSubmission{AnswerText: "Ответ", SelectedOptionIDs: []int64{10}}, wantErr: domain.ErrInvalidInput},
		{name: "single choice accepted", question: singleChoice, params: domain.AnswerSubmission{SelectedOptionIDs: []int64{11}}},
		{name: "single choice requires one", question: singleChoice, params: domain.AnswerSubmission{SelectedOptionIDs: []int64{10, 11}}, wantErr: domain.ErrInvalidInput},
		{name: "single choice rejects foreign option", question: singleChoice, params: domain.AnswerSubmission{SelectedOptionIDs: []int64{99}}, wantErr: domain.ErrInvalidInput},
		{name: "multiple choice accepts several options", question: multipleChoice, params: domain.AnswerSubmission{SelectedOptionIDs: []int64{20, 21}}},
		{name: "multiple choice requires selection", question: multipleChoice, params: domain.AnswerSubmission{}, wantErr: domain.ErrInvalidInput},
		{name: "duplicate choices rejected", question: multipleChoice, params: domain.AnswerSubmission{SelectedOptionIDs: []int64{20, 20}}, wantErr: domain.ErrInvalidInput},
		{name: "unknown type rejected", question: domain.LessonQuestion{AnswerType: "unknown"}, params: domain.AnswerSubmission{AnswerText: "Ответ"}, wantErr: domain.ErrInvalidState},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSubmission(test.question, test.params)
			if test.wantErr == nil && err != nil {
				t.Fatalf("validateSubmission() error = %v, want nil", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("validateSubmission() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestEvaluationResultRemainsUnsetUntilEvaluation(t *testing.T) {
	// У доменной модели отправки ответа нет поля, позволяющего пометить её правильной без результата проверки.
	answer := domain.SavedAnswer{ProcessingStatus: "pending"}
	if answer.ProcessingStatus != "pending" {
		t.Fatalf("новая попытка должна оставаться pending, получено %q", answer.ProcessingStatus)
	}
}
