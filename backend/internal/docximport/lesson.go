package docximport

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrInvalidLessonStructure = errors.New("invalid lesson document structure")
	questionHeaderPattern    = regexp.MustCompile(`^Вопрос\s+([^:]+):\s*(.*)$`)
	bibleReferencePattern    = regexp.MustCompile(`^(.+?)\s+(\d+):(\d+)(?:\s*[-–—]\s*(\d+))?$`)
	answerMarkerPattern      = regexp.MustCompile(`^\s*\[\s*(.*?)\s*\]\s*(.*?)\s*$`)
)

const (
	maxLessonTitleBytes    = 200
	maxLessonIntroBytes    = 4000
	maxQuestionPromptBytes = 4000
	maxQuestionTextBytes   = 4000
	maxQuestionsPerLesson  = 200
	maxOptionsPerQuestion  = 20
	maxBiblePassages       = 20
	maxOptionTextBytes     = 1000
)

// LessonImport is a fully validated, in-memory lesson representation.
// Parsing does not write to the database or publish lesson content.
type LessonImport struct {
	Title       string
	Description *string
	Questions   []QuestionImport
}

// QuestionImport contains one question and the materials explicitly found in the document.
type QuestionImport struct {
	Number              int
	Prompt               string
	AnswerType           string
	ExpectedAnswer       string
	ExpectedExplanation  *string
	Note                 *string
	Options              []OptionImport
	BiblePassages        []BiblePassageImport
	bibleReferences      string
	scriptureText        string
	seenFields           map[string]bool
	choiceMarkerType     string
}

type OptionImport struct {
	Text      string
	IsCorrect bool
}

type BiblePassageImport struct {
	Reference   string
	BookName    string
	Chapter     int
	StartVerse  int
	EndVerse    int
	PassageText *string
}

// ParseLesson parses text extracted from DOCX using the labels used in biblical lessons.
// It validates the complete lesson before returning an import representation.
func ParseLesson(text string) (LessonImport, error) {
	if strings.TrimSpace(text) == "" || len(text) > maxExtractedBytes {
		if len(text) > maxExtractedBytes {
			return LessonImport{}, ErrTooLarge
		}
		return LessonImport{}, lessonError(0, "документ пуст")
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	titleSeen, introSeen, introEnded := false, false, false
	var result LessonImport
	var intro []string
	var current *QuestionImport
	var currentField string

	finishQuestion := func() error {
		if current == nil {
			return nil
		}
		if err := validateImportedQuestion(current); err != nil {
			return err
		}
		result.Questions = append(result.Questions, *current)
		current = nil
		currentField = ""
		if len(result.Questions) > maxQuestionsPerLesson {
			return lessonError(0, fmt.Sprintf("превышено максимальное количество вопросов (%d)", maxQuestionsPerLesson))
		}
		return nil
	}

	for lineIndex, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			if current != nil && currentField != "" {
				appendQuestionFieldLine(current, currentField, "")
			} else if introSeen && !introEnded {
				intro = append(intro, "")
			}
			continue
		}

		if strings.HasPrefix(line, "Название урока:") {
			if titleSeen || len(result.Questions) > 0 || current != nil {
				return LessonImport{}, lessonError(0, "название урока повторяется или находится не в начале документа")
			}
			title := strings.TrimSpace(strings.TrimPrefix(line, "Название урока:"))
			if title == "" || len(title) > maxLessonTitleBytes {
				return LessonImport{}, lessonError(0, "название урока отсутствует или превышает допустимую длину")
			}
			result.Title = title
			titleSeen = true
			continue
		}

		if strings.HasPrefix(line, "Вводный текст:") {
			if !titleSeen || introSeen || current != nil || len(result.Questions) > 0 {
				return LessonImport{}, lessonError(0, "поле «Вводный текст» повторяется или расположено неверно")
			}
			introSeen = true
			introEnded = false
			value := strings.TrimSpace(strings.TrimPrefix(line, "Вводный текст:"))
			if value != "" {
				intro = append(intro, value)
			}
			continue
		}

		if strings.HasPrefix(line, "Вопрос") {
			match := questionHeaderPattern.FindStringSubmatch(line)
			if match == nil {
				return LessonImport{}, lessonError(0, fmt.Sprintf("строка %d: некорректный маркер вопроса", lineIndex+1))
			}
			if !titleSeen {
				return LessonImport{}, lessonError(0, "отсутствует название урока перед вопросами")
			}
			if err := finishQuestion(); err != nil {
				return LessonImport{}, err
			}
			numberText := strings.TrimSpace(match[1])
			number, err := strconv.Atoi(numberText)
			if err != nil || number <= 0 {
				return LessonImport{}, lessonError(0, fmt.Sprintf("некорректный номер вопроса %q", numberText))
			}
			for _, existing := range result.Questions {
				if existing.Number == number {
					return LessonImport{}, lessonError(number, "номер вопроса повторяется")
				}
			}
			prompt := strings.TrimSpace(match[2])
			if prompt == "" || len(prompt) > maxQuestionPromptBytes {
				return LessonImport{}, lessonError(number, "формулировка вопроса отсутствует или слишком длинная")
			}
			current = &QuestionImport{Number: number, Prompt: prompt, seenFields: make(map[string]bool)}
			currentField = ""
			introEnded = true
			continue
		}

		if current == nil {
			if !titleSeen {
				return LessonImport{}, lessonError(0, fmt.Sprintf("строка %d: ожидается «Название урока:»", lineIndex+1))
			}
			if introSeen && !introEnded {
				intro = append(intro, line)
				continue
			}
			return LessonImport{}, lessonError(0, fmt.Sprintf("строка %d: текст не относится ни к одному полю", lineIndex+1))
		}

		if fieldName, value, ok := parseQuestionField(line); ok {
			if current.seenFields[fieldName] {
				return LessonImport{}, lessonError(current.Number, fmt.Sprintf("поле «%s» повторяется", fieldName))
			}
			current.seenFields[fieldName] = true
			currentField = fieldName
			if value != "" {
				appendQuestionFieldLine(current, currentField, value)
			}
			continue
		}

		if strings.HasPrefix(line, "[") {
			match := answerMarkerPattern.FindStringSubmatch(line)
			if match == nil {
				return LessonImport{}, lessonError(current.Number, "некорректный маркер варианта ответа")
			}
			marker := strings.ToLower(strings.Join(strings.Fields(match[1]), " "))
			markerType := ""
			isCorrect := false
			switch marker {
			case "":
			case "правильный ответ":
				markerType, isCorrect = "single_choice", true
			case "правильные ответы":
				markerType, isCorrect = "multiple_choice", true
			default:
				return LessonImport{}, lessonError(current.Number, fmt.Sprintf("неизвестный маркер ответа [%s]", match[1]))
			}
			if markerType != "" {
				if current.choiceMarkerType != "" && current.choiceMarkerType != markerType {
					current.choiceMarkerType = "mixed_choice"
				} else if current.choiceMarkerType == "" {
					current.choiceMarkerType = markerType
				}
			}
			optionText := strings.TrimSpace(match[2])
			if optionText == "" {
				return LessonImport{}, lessonError(current.Number, "вариант ответа пуст")
			}
			if len(optionText) > maxOptionTextBytes {
				return LessonImport{}, lessonError(current.Number, "текст варианта ответа слишком длинный")
			}
			if len(current.Options) >= maxOptionsPerQuestion {
				return LessonImport{}, lessonError(current.Number, fmt.Sprintf("превышено максимальное количество вариантов (%d)", maxOptionsPerQuestion))
			}
			current.Options = append(current.Options, OptionImport{Text: optionText, IsCorrect: isCorrect})
			currentField = ""
			continue
		}

		if currentField == "" {
			return LessonImport{}, lessonError(current.Number, fmt.Sprintf("строка %d: текст не распознан как поле или вариант ответа", lineIndex+1))
		}
		appendQuestionFieldLine(current, currentField, line)
	}

	if !titleSeen {
		return LessonImport{}, lessonError(0, "отсутствует название урока")
	}
	if err := finishQuestion(); err != nil {
		return LessonImport{}, err
	}
	if introSeen {
		value := strings.TrimSpace(strings.Join(intro, "\n"))
		if value == "" {
			return LessonImport{}, lessonError(0, "поле «Вводный текст» пусто")
		}
		if len(value) > maxLessonIntroBytes {
			return LessonImport{}, lessonError(0, "вводный текст превышает допустимую длину")
		}
		result.Description = &value
	}
	if len(result.Questions) == 0 {
		return LessonImport{}, lessonError(0, "документ не содержит вопросов")
	}
	return result, nil
}

func parseQuestionField(line string) (name, value string, ok bool) {
	fields := []string{
		"Библейские ссылки:",
		"Текст Писания:",
		"Предполагаемый ответ:",
		"Объяснение:",
		"Пометка:",
	}
	for _, field := range fields {
		if strings.HasPrefix(line, field) {
			return strings.TrimSuffix(field, ":"), strings.TrimSpace(strings.TrimPrefix(line, field)), true
		}
	}
	return "", "", false
}

func appendQuestionFieldLine(question *QuestionImport, field, line string) {
	switch field {
	case "Библейские ссылки":
		question.bibleReferences = appendTextLine(question.bibleReferences, line)
	case "Текст Писания":
		question.scriptureText = appendTextLine(question.scriptureText, line)
	case "Предполагаемый ответ":
		question.ExpectedAnswer = appendTextLine(question.ExpectedAnswer, line)
	case "Объяснение":
		value := ""
		if question.ExpectedExplanation != nil {
			value = *question.ExpectedExplanation
		}
		value = appendTextLine(value, line)
		question.ExpectedExplanation = &value
	case "Пометка":
		value := ""
		if question.Note != nil {
			value = *question.Note
		}
		value = appendTextLine(value, line)
		question.Note = &value
	}
}

func appendTextLine(existing, line string) string {
	if existing == "" {
		return line
	}
	if line == "" {
		return existing + "\n"
	}
	return existing + "\n" + line
}

func validateImportedQuestion(question *QuestionImport) error {
	for field, value := range map[string]string{
		"Библейские ссылки": question.bibleReferences,
		"Текст Писания": question.scriptureText,
		"Предполагаемый ответ": question.ExpectedAnswer,
		"Объяснение": valueOf(question.ExpectedExplanation),
		"Пометка": valueOf(question.Note),
	} {
		if question.seenFields[field] && strings.TrimSpace(value) == "" {
			return lessonError(question.Number, fmt.Sprintf("поле «%s» пусто", field))
		}
	}
	if len(question.Options) == 0 {
		if question.choiceMarkerType != "" {
			return lessonError(question.Number, "тип вопроса не определён однозначно")
		}
		if strings.TrimSpace(question.ExpectedAnswer) == "" {
			return lessonError(question.Number, "у свободного ответа отсутствует поле «Предполагаемый ответ»")
		}
		if len(question.ExpectedAnswer) > maxQuestionTextBytes {
			return lessonError(question.Number, "предполагаемый ответ слишком длинный")
		}
		question.AnswerType = "text"
	} else {
		if strings.TrimSpace(question.ExpectedAnswer) != "" {
			return lessonError(question.Number, "нельзя одновременно указывать предполагаемый ответ и варианты выбора")
		}
		if question.choiceMarkerType == "mixed_choice" {
			return lessonError(question.Number, "смешаны маркеры единственного и множественного выбора")
		}
		correct := 0
		for _, option := range question.Options {
			if option.IsCorrect {
				correct++
			}
		}
		switch question.choiceMarkerType {
		case "single_choice":
			if correct == 0 {
				return lessonError(question.Number, "у вопроса с единственным выбором нет правильного ответа")
			}
			if correct != 1 {
				return lessonError(question.Number, "у вопроса с единственным выбором указано несколько правильных ответов")
			}
		case "multiple_choice":
			if correct == 0 {
				return lessonError(question.Number, "у вопроса с множественным выбором нет правильного ответа")
			}
		default:
			return lessonError(question.Number, "у вопроса с вариантами ответа отсутствует маркер правильного ответа")
		}
		question.AnswerType = question.choiceMarkerType
	}

	if len(question.bibleReferences) > maxQuestionTextBytes ||
		len(question.scriptureText) > maxQuestionTextBytes ||
		len(valueOf(question.ExpectedExplanation)) > maxQuestionTextBytes ||
		len(valueOf(question.Note)) > maxQuestionTextBytes {
		return lessonError(question.Number, "текст одного из дополнительных полей слишком длинный")
	}

	if strings.TrimSpace(question.scriptureText) != "" && strings.TrimSpace(question.bibleReferences) == "" {
		return lessonError(question.Number, "текст Писания указан без библейской ссылки")
	}
	if strings.TrimSpace(question.bibleReferences) != "" {
		references := splitBibleReferences(question.bibleReferences)
		if len(references) == 0 || len(references) > maxBiblePassages {
			return lessonError(question.Number, fmt.Sprintf("количество библейских ссылок должно быть от 1 до %d", maxBiblePassages))
		}
		for _, reference := range references {
			passage, err := parseBibleReference(reference, strings.TrimSpace(question.scriptureText))
			if err != nil {
				return lessonError(question.Number, err.Error())
			}
			question.BiblePassages = append(question.BiblePassages, passage)
		}
	}
	if question.ExpectedExplanation != nil {
		value := strings.TrimSpace(*question.ExpectedExplanation)
		question.ExpectedExplanation = &value
	}
	if question.Note != nil {
		value := strings.TrimSpace(*question.Note)
		question.Note = &value
	}
	return nil
}

func splitBibleReferences(text string) []string {
	var references []string
	for _, line := range strings.Split(text, "\n") {
		for _, item := range strings.Split(line, ";") {
			if value := strings.TrimSpace(item); value != "" {
				references = append(references, value)
			}
		}
	}
	return references
}

func parseBibleReference(reference, scriptureText string) (BiblePassageImport, error) {
	match := bibleReferencePattern.FindStringSubmatch(strings.TrimSpace(reference))
	if match == nil {
		return BiblePassageImport{}, fmt.Errorf("некорректная библейская ссылка %q; ожидается формат «Книга глава:стих»", reference)
	}
	chapter, errChapter := strconv.Atoi(match[2])
	startVerse, errStart := strconv.Atoi(match[3])
	endVerse := startVerse
	var errEnd error
	if match[4] != "" {
		endVerse, errEnd = strconv.Atoi(match[4])
	}
	if errChapter != nil || errStart != nil || errEnd != nil || chapter <= 0 || startVerse <= 0 || endVerse < startVerse {
		return BiblePassageImport{}, fmt.Errorf("некорректный диапазон в библейской ссылке %q", reference)
	}
	passage := BiblePassageImport{
		Reference: reference, BookName: strings.TrimSpace(match[1]),
		Chapter: chapter, StartVerse: startVerse, EndVerse: endVerse,
	}
	if scriptureText != "" {
		value := scriptureText
		passage.PassageText = &value
	}
	return passage, nil
}

func valueOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func lessonError(questionNumber int, problem string) error {
	if questionNumber > 0 {
		return fmt.Errorf("%w: вопрос %d: %s", ErrInvalidLessonStructure, questionNumber, problem)
	}
	return fmt.Errorf("%w: %s", ErrInvalidLessonStructure, problem)
}
