package docximport

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLessonSupportsSingleChoice(t *testing.T) {
	input := `Название урока: Библия — забытое сокровище

Вводный текст:
Текст вступления к уроку.
Второй абзац вступления.

Вопрос 1: Что, по словам Христа, является истиной?

Библейские ссылки:
Иоанна 17:17

Текст Писания:
Освяти их истиною Твоею;
слово Твое есть истина.

Объяснение:
Пояснение автора урока.

[] Слова святых людей
[] Учение церкви
[правильный ответ] Слово Божье`

	got, err := ParseLesson(input)
	if err != nil {
		t.Fatalf("ParseLesson() error = %v", err)
	}
	if got.Title != "Библия — забытое сокровище" || got.Description == nil ||
		*got.Description != "Текст вступления к уроку.\nВторой абзац вступления." {
		t.Fatalf("unexpected lesson metadata: %+v", got)
	}
	if len(got.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(got.Questions))
	}
	q := got.Questions[0]
	if q.Number != 1 || q.AnswerType != "single_choice" || len(q.Options) != 3 ||
		q.Options[0].Text != "Слова святых людей" || q.Options[0].IsCorrect ||
		q.Options[1].Text != "Учение церкви" || q.Options[1].IsCorrect ||
		q.Options[2].Text != "Слово Божье" || !q.Options[2].IsCorrect {
		t.Fatalf("unexpected single-choice question: %+v", q)
	}
	if q.ExpectedExplanation == nil || *q.ExpectedExplanation != "Пояснение автора урока." {
		t.Fatalf("explanation = %v", q.ExpectedExplanation)
	}
	if len(q.BiblePassages) != 1 || q.BiblePassages[0].BookName != "Иоанна" ||
		q.BiblePassages[0].Chapter != 17 || q.BiblePassages[0].StartVerse != 17 ||
		q.BiblePassages[0].EndVerse != 17 || q.BiblePassages[0].PassageText == nil ||
		*q.BiblePassages[0].PassageText != "Освяти их истиною Твоею;\nслово Твое есть истина." {
		t.Fatalf("unexpected Bible passage: %+v", q.BiblePassages)
	}
}

func TestParseLessonSupportsMultipleChoiceAndNote(t *testing.T) {
	input := `Название урока: Проверка

Вопрос 4: Для чего нам предлагается изучать Слово Божье?

Библейские ссылки:
2 Тимофею 3:16–17
Иоанна 17:17

Текст Писания:
Текст отрывка.

Пометка:
Дополнительная информация для вопроса.

[] Чтобы чувствовать постоянный страх
[правильные ответы] Чтобы совершенствоваться
[правильные ответы] Чтобы быть готовыми к добрым делам`
	got, err := ParseLesson(input)
	if err != nil {
		t.Fatalf("ParseLesson() error = %v", err)
	}
	q := got.Questions[0]
	if q.AnswerType != "multiple_choice" || len(q.Options) != 3 ||
		q.Options[0].IsCorrect || !q.Options[1].IsCorrect || !q.Options[2].IsCorrect {
		t.Fatalf("unexpected multiple-choice question: %+v", q)
	}
	if q.Note == nil || *q.Note != "Дополнительная информация для вопроса." {
		t.Fatalf("note = %v", q.Note)
	}
	if len(q.BiblePassages) != 2 || q.BiblePassages[0].BookName != "2 Тимофею" ||
		q.BiblePassages[0].StartVerse != 16 || q.BiblePassages[0].EndVerse != 17 ||
		q.BiblePassages[1].BookName != "Иоанна" || q.BiblePassages[1].PassageText == nil {
		t.Fatalf("unexpected Bible passages: %+v", q.BiblePassages)
	}
}

func TestParseLessonSupportsFreeTextAnswer(t *testing.T) {
	input := `Название урока: Свободный ответ

Вопрос 3: Какой силой были наделены пророки Господни?

Предполагаемый ответ:
Святые люди Божьи были движимы Духом Святым.

Объяснение:
Первый абзац объяснения.
Второй абзац объяснения.`
	got, err := ParseLesson(input)
	if err != nil {
		t.Fatalf("ParseLesson() error = %v", err)
	}
	q := got.Questions[0]
	if q.AnswerType != "text" || q.ExpectedAnswer != "Святые люди Божьи были движимы Духом Святым." || len(q.Options) != 0 {
		t.Fatalf("unexpected free-text question: %+v", q)
	}
	if q.ExpectedExplanation == nil || *q.ExpectedExplanation != "Первый абзац объяснения.\nВторой абзац объяснения." {
		t.Fatalf("explanation = %v", q.ExpectedExplanation)
	}
}

func TestParseLessonAllowsOptionalQuestionFields(t *testing.T) {
	got, err := ParseLesson(`Название урока: Минимальный урок

Вопрос 1: Ответьте своими словами.

Предполагаемый ответ:
Свободный ответ.`)
	if err != nil {
		t.Fatalf("ParseLesson() error = %v", err)
	}
	q := got.Questions[0]
	if got.Description != nil || q.ExpectedExplanation != nil || q.Note != nil || len(q.BiblePassages) != 0 {
		t.Fatalf("unexpected optional fields: lesson=%+v question=%+v", got, q)
	}
}

func TestParseLessonRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"missing title", "Вопрос 1: Вопрос\nПредполагаемый ответ: Ответ", "название урока"},
		{"malformed question number", "Название урока: Тест\nВопрос abc: Вопрос\nПредполагаемый ответ: Ответ", "номер вопроса"},
		{"duplicate question number", "Название урока: Тест\nВопрос 1: Первый\nПредполагаемый ответ: Ответ\nВопрос 1: Второй\nПредполагаемый ответ: Ответ", "повторяется"},
		{"missing prompt", "Название урока: Тест\nВопрос 1:\nПредполагаемый ответ: Ответ", "формулировка"},
		{"no correct answer", "Название урока: Тест\nВопрос 1: Выберите\n[] Первый\n[] Второй", "нет правильного ответа"},
		{"multiple correct answers for single choice", "Название урока: Тест\nВопрос 1: Выберите\n[правильный ответ] Первый\n[правильный ответ] Второй", "несколько правильных"},
		{"mixed markers", "Название урока: Тест\nВопрос 1: Выберите\n[правильный ответ] Первый\n[правильные ответы] Второй", "смешаны маркеры"},
		{"empty option", "Название урока: Тест\nВопрос 1: Выберите\n[правильный ответ] ", "формулировка"},
		{"missing expected answer", "Название урока: Тест\nВопрос 1: Ответьте\nОбъяснение: Пояснение", "Предполагаемый ответ"},
		{"expected answer and options", "Название урока: Тест\nВопрос 1: Выберите\nПредполагаемый ответ: Эталон\n[правильный ответ] Верно\n[] Неверно", "одновременно"},
		{"unknown marker", "Название урока: Тест\nВопрос 1: Выберите\n[верный ответ] Верно\n[] Неверно", "неизвестный маркер"},
		{"empty note field", "Название урока: Тест\nВопрос 1: Ответьте\nПредполагаемый ответ: Ответ\nПометка:", "поле «Пометка» пусто"},
		{"bad Bible reference", "Название урока: Тест\nВопрос 1: Ответьте\nПредполагаемый ответ: Ответ\nБиблейские ссылки: Иоанна XVII:17", "некорректная библейская ссылка"},
		{"scripture without reference", "Название урока: Тест\nВопрос 1: Ответьте\nПредполагаемый ответ: Ответ\nТекст Писания: Текст", "без библейской ссылки"},
		{"no questions", "Название урока: Тест\nВводный текст: Вступление", "не содержит вопросов"},
		{"empty introduction", "Название урока: Тест\nВводный текст: \nВопрос 1: Ответьте\nПредполагаемый ответ: Ответ", "Вводный текст» пусто"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseLesson(tt.text)
			if !errors.Is(err, ErrInvalidLessonStructure) {
				t.Fatalf("ParseLesson() error = %v, want ErrInvalidLessonStructure", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestParseLessonPreservesQuestionAndOptionOrder(t *testing.T) {
	got, err := ParseLesson(`Название урока: Порядок

Вопрос 5: Второй по порядку в документе
[] Неверно
[правильный ответ] Верно

Вопрос 2: Первый по смыслу, второй в документе
[правильный ответ] Да
[] Нет`)
	if err != nil {
		t.Fatalf("ParseLesson() error = %v", err)
	}
	if len(got.Questions) != 2 || got.Questions[0].Number != 5 || got.Questions[1].Number != 2 {
		t.Fatalf("question order changed: %+v", got.Questions)
	}
	if got.Questions[0].Options[0].Text != "Неверно" || got.Questions[0].Options[1].Text != "Верно" {
		t.Fatalf("option order changed: %+v", got.Questions[0].Options)
	}
}

func TestParseLessonRejectsTooLargeInput(t *testing.T) {
	_, err := ParseLesson(strings.Repeat("x", maxExtractedBytes+1))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ParseLesson() error = %v, want ErrTooLarge", err)
	}
}
