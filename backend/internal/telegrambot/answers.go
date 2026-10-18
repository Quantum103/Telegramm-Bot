package telegrambot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

type answerStateKey struct {
	studentID    int64
	enrollmentID int64
	questionID   int64
}

type answerLearningService interface {
	CurrentQuestion(context.Context, int64) (service.NavigationState, error)
	SubmitAnswer(context.Context, domain.AnswerSubmission) (service.SubmissionResult, error)
}

type telegramMessageDeleter interface {
	DeleteMessage(context.Context, int64, int64) error
}

func (b *Bot) rememberMainMessage(chatID, messageID int64) {
	if chatID <= 0 || messageID <= 0 {
		return
	}
	b.messageMu.Lock()
	b.mainMessages[chatID] = messageID
	b.messageMu.Unlock()
}

func (b *Bot) knownMainMessage(chatID int64) int64 {
	b.messageMu.RLock()
	defer b.messageMu.RUnlock()
	return b.mainMessages[chatID]
}

func (b *Bot) isChoiceEditing(key answerStateKey) bool {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	return b.choiceEditing[key]
}

func (b *Bot) setChoiceEditing(key answerStateKey, editing bool) {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	if editing {
		b.choiceEditing[key] = true
		return
	}
	delete(b.choiceEditing, key)
}

func (b *Bot) clearChoiceState(key answerStateKey) {
	b.choiceMu.Lock()
	delete(b.choiceEditing, key)
	delete(b.multiDrafts, key)
	b.choiceMu.Unlock()
}

func (b *Bot) seedMultiDraft(key answerStateKey, optionIDs []int64) {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	selected := make(map[int64]struct{}, len(optionIDs))
	for _, id := range optionIDs {
		selected[id] = struct{}{}
	}
	b.multiDrafts[key] = selected
}

func (b *Bot) toggleMultiDraft(key answerStateKey, optionID int64) {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	selected, exists := b.multiDrafts[key]
	if !exists {
		selected = make(map[int64]struct{})
		b.multiDrafts[key] = selected
	}
	if _, checked := selected[optionID]; checked {
		delete(selected, optionID)
	} else {
		selected[optionID] = struct{}{}
	}
}

func (b *Bot) multiDraftSelection(key answerStateKey) []int64 {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	selected := b.multiDrafts[key]
	result := make([]int64, 0, len(selected))
	for id := range selected {
		result = append(result, id)
	}
	return result
}

func (b *Bot) handleTextAnswer(ctx context.Context, updateID int64, user User, message Message, text string) {
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Warn("не удалось определить ученика для текстового ответа", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте ещё раз.")
		return
	}
	answerService, ok := b.learning.(answerLearningService)
	if !ok {
		b.send(ctx, message.Chat.ID, "Не удалось проверить текущий вопрос. Откройте урок через /start и попробуйте снова.")
		return
	}

	state, err := answerService.CurrentQuestion(ctx, student.ID)
	if err != nil {
		b.logger.Warn("нет доступного текущего вопроса для текстового ответа", "student_id", student.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Сейчас нет открытого текстового вопроса. Отправьте /start и выберите курс и урок.")
		return
	}
	if state.Question.AnswerType != "text" {
		b.send(ctx, message.Chat.ID, "Для этого вопроса используйте кнопки вариантов ответа.")
		return
	}
	if state.Progress.Enrollment.CurrentQuestionID == nil ||
		*state.Progress.Enrollment.CurrentQuestionID != state.Question.ID ||
		state.Progress.Enrollment.Status != "in_progress" {
		b.send(ctx, message.Chat.ID, "Текущий вопрос изменился. Откройте его через кнопки и отправьте ответ ещё раз.")
		return
	}
	if strings.TrimSpace(text) == "" {
		b.send(ctx, message.Chat.ID, "Пустой ответ не сохранён. Напишите ответ и отправьте его ещё раз.")
		return
	}

	result, err := answerService.SubmitAnswer(ctx, domain.AnswerSubmission{
		StudentID: student.ID, EnrollmentID: state.Progress.Enrollment.ID,
		QuestionID: state.Question.ID, AnswerText: strings.TrimSpace(text),
		IdempotencyKey: telegramUpdateIdempotencyKey(updateID),
	})
	if err != nil {
		b.logger.Warn("не удалось сохранить текстовый ответ", "student_id", student.ID,
			"enrollment_id", state.Progress.Enrollment.ID, "question_id", state.Question.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Не удалось сохранить ответ. Он не принят. Проверьте соединение и отправьте ответ ещё раз.")
		return
	}

	// Сохранение уже завершено. Ошибка удаления сообщения Telegram не должна отменять ответ.
	b.deleteIncomingMessage(ctx, message)
	refreshed, refreshErr := b.learning.OpenQuestion(ctx, student.ID, result.Progress.Enrollment.ID, state.Question.ID)
	if refreshErr != nil {
		state.SavedAnswer = &result.Answer
		state.Progress = result.Progress
	} else {
		state = refreshed
	}
	b.renderQuestion(ctx, message.Chat.ID, student.ID, b.knownMainMessage(message.Chat.ID), state, nil, 0)
}

func (b *Bot) deleteIncomingMessage(ctx context.Context, message Message) {
	deleter, ok := b.api.(telegramMessageDeleter)
	if !ok {
		return
	}
	if err := deleter.DeleteMessage(ctx, message.Chat.ID, message.MessageID); err != nil && ctx.Err() == nil {
		b.logger.Warn("не удалось удалить исходное сообщение с ответом; сохранённый ответ оставлен без изменений",
			"chat_id", message.Chat.ID, "message_id", message.MessageID, "error", err)
	}
}

func telegramUpdateIdempotencyKey(updateID int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("telegram-update:%d", updateID)))
	value := append([]byte(nil), digest[:16]...)
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func (b *Bot) handleAnswerCallback(ctx context.Context, updateID, chatID, studentID, messageID int64, parts []string) {
	action := parts[0]
	switch action {
	case "e":
		action = "pick-edit"
	case "p":
		action = "pick"
	case "t":
		action = "multi-toggle"
	case "s":
		action = "multi-submit"
	}
	need := 0
	switch action {
	case "pick-edit":
		need = 3
	case "pick", "multi-toggle":
		need = 4
	case "multi-submit":
		need = 3
	default:
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}
	if len(parts) != need {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}
	enrollmentID, okEnrollment := parsePositiveID(parts[1])
	questionID, okQuestion := parsePositiveID(parts[2])
	if !okEnrollment || !okQuestion {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}
	var optionID int64
	if action == "pick" || action == "multi-toggle" {
		var ok bool
		optionID, ok = parsePositiveID(parts[3])
		if !ok {
			b.showStaleCallback(ctx, chatID, studentID, messageID)
			return
		}
	}

	progress, err := b.learning.GetProgress(ctx, studentID, enrollmentID)
	if err != nil || progress.Enrollment.Status != "in_progress" ||
		progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != questionID {
		if err != nil {
			b.showStaleCallback(ctx, chatID, studentID, messageID)
		} else {
			b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID, 0)
		}
		return
	}
	state, err := b.learning.OpenQuestion(ctx, studentID, enrollmentID, questionID)
	if err != nil {
		b.logger.Warn("не удалось проверить вопрос для ответа кнопкой", "student_id", studentID,
			"enrollment_id", enrollmentID, "question_id", questionID, "error", err)
		b.send(ctx, chatID, "Не удалось проверить текущий вопрос. Попробуйте открыть его ещё раз.")
		return
	}
	key := answerStateKey{studentID: studentID, enrollmentID: enrollmentID, questionID: questionID}
	switch action {
	case "pick-edit":
		if state.Question.AnswerType != "single_choice" && state.Question.AnswerType != "multiple_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		b.setChoiceEditing(key, true)
		if state.Question.AnswerType == "multiple_choice" && state.SavedAnswer != nil {
			b.seedMultiDraft(key, state.SavedAnswer.SelectedOptionIDs)
		}
		b.renderQuestionMode(ctx, chatID, studentID, messageID, state, nil, 0, true)
		return
	case "pick":
		if state.Question.AnswerType != "single_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		if state.SavedAnswer != nil && !b.isChoiceEditing(key) {
			b.send(ctx, chatID, "Ответ уже сохранён. Нажмите «Изменить ответ», чтобы выбрать другой вариант.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		if !questionHasOption(state.Question.Options, optionID) {
			b.send(ctx, chatID, "Выбранный вариант не относится к текущему вопросу. Выберите вариант на экране вопроса.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		b.saveChoiceAnswer(ctx, updateID, chatID, studentID, messageID, state, []int64{optionID})
		return
	case "multi-toggle":
		if state.Question.AnswerType != "multiple_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		if state.SavedAnswer != nil && !b.isChoiceEditing(key) {
			b.send(ctx, chatID, "Ответ уже сохранён. Нажмите «Изменить ответ», чтобы изменить выбор.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		if !questionHasOption(state.Question.Options, optionID) {
			b.send(ctx, chatID, "Выбранный вариант не относится к текущему вопросу.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		b.toggleMultiDraft(key, optionID)
		b.setChoiceEditing(key, true)
		b.renderQuestionMode(ctx, chatID, studentID, messageID, state, nil, 0, true)
		return
	case "multi-submit":
		if state.Question.AnswerType != "multiple_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		if state.SavedAnswer != nil && !b.isChoiceEditing(key) {
			b.send(ctx, chatID, "Ответ уже сохранён. Нажмите «Изменить ответ», чтобы изменить выбор.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return
		}
		selected := orderedDraftSelection(state.Question.Options, b.multiDraftSelection(key))
		if len(selected) == 0 {
			b.send(ctx, chatID, "Сначала отметьте хотя бы один вариант, затем подтвердите выбор.")
			return
		}
		b.saveChoiceAnswer(ctx, updateID, chatID, studentID, messageID, state, selected)
		return
	}
}

func (b *Bot) saveChoiceAnswer(ctx context.Context, updateID, chatID, studentID, messageID int64, state service.NavigationState, selected []int64) {
	answerService, ok := b.learning.(answerLearningService)
	if !ok {
		b.send(ctx, chatID, "Не удалось сохранить выбор. Попробуйте ещё раз.")
		return
	}
	enrollmentID := state.Progress.Enrollment.ID
	questionID := state.Question.ID
	result, err := answerService.SubmitAnswer(ctx, domain.AnswerSubmission{
		StudentID:         studentID,
		EnrollmentID:      enrollmentID,
		QuestionID:        questionID,
		SelectedOptionIDs: append([]int64(nil), selected...),
		IdempotencyKey:    telegramUpdateIdempotencyKey(updateID),
	})
	if err != nil {
		b.logger.Warn("не удалось сохранить выбор ответа", "student_id", studentID,
			"enrollment_id", enrollmentID, "question_id", questionID, "error", err)
		b.send(ctx, chatID, "Не удалось сохранить выбор. Ответ не принят; попробуйте подтвердить его ещё раз.")
		return
	}
	b.clearChoiceState(answerStateKey{studentID: studentID, enrollmentID: enrollmentID, questionID: questionID})
	refreshed, refreshErr := b.learning.OpenQuestion(ctx, studentID, enrollmentID, questionID)
	if refreshErr != nil {
		state.SavedAnswer = &result.Answer
		state.Progress = result.Progress
	} else {
		state = refreshed
	}
	b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
}

func questionHasOption(options []domain.QuestionOption, selectedID int64) bool {
	for _, option := range options {
		if option.ID == selectedID {
			return true
		}
	}
	return false
}

func orderedDraftSelection(options []domain.QuestionOption, ids []int64) []int64 {
	selected := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	result := make([]int64, 0, len(ids))
	for _, option := range options {
		if _, ok := selected[option.ID]; ok {
			result = append(result, option.ID)
		}
	}
	return result
}
