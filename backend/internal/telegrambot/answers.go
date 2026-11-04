package telegrambot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
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

// setMultiDraftOption applies the desired state, so redelivery of one callback
// cannot invert the selection a second time.
func (b *Bot) setMultiDraftOption(key answerStateKey, optionID int64, checked bool) {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	selected, exists := b.multiDrafts[key]
	if !exists {
		selected = make(map[int64]struct{})
		b.multiDrafts[key] = selected
	}
	if checked {
		selected[optionID] = struct{}{}
		return
	}
	delete(selected, optionID)
}

// seedMultiDraftFromMarkup restores a volatile draft after a process restart.
// Callback data stores the action's target state; for older keyboards, the
// selected marker in the button label is used as a compatibility fallback.
func (b *Bot) seedMultiDraftFromMarkup(key answerStateKey, markup *InlineKeyboardMarkup) {
	b.choiceMu.Lock()
	defer b.choiceMu.Unlock()
	if _, exists := b.multiDrafts[key]; exists {
		return
	}
	selected := make(map[int64]struct{})
	if markup != nil {
		for _, row := range markup.InlineKeyboard {
			for _, item := range row {
				parts := strings.Split(item.CallbackData, ":")
				if len(parts) < 4 || parts[0] != "t" {
					continue
				}
				enrollmentID, errEnrollment := strconv.ParseInt(parts[1], 10, 64)
				questionID, errQuestion := strconv.ParseInt(parts[2], 10, 64)
				optionID, errOption := strconv.ParseInt(parts[3], 10, 64)
				if errEnrollment != nil || errQuestion != nil || errOption != nil ||
					enrollmentID != key.enrollmentID || questionID != key.questionID || optionID <= 0 {
					continue
				}
				if len(parts) == 5 {
					switch parts[4] {
					case "0":
						selected[optionID] = struct{}{}
					case "1":
						delete(selected, optionID)
					}
				} else if strings.HasPrefix(item.Text, "✅ ") {
					selected[optionID] = struct{}{}
				}
			}
		}
	}
	b.multiDrafts[key] = selected
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


func (b *Bot) handleTextAnswer(ctx context.Context, updateID int64, user User, message Message, text string) bool {
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Warn("не удалось определить ученика для текстового ответа", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте ещё раз.")
		return !isRetryableUpdateError(err)
	}
	answerService, ok := b.learning.(answerLearningService)
	if !ok {
		b.send(ctx, message.Chat.ID, "Не удалось проверить текущий вопрос. Откройте урок через /start и попробуйте снова.")
		return true
	}

	state, err := answerService.CurrentQuestion(ctx, student.ID)
	if err != nil {
		b.logger.Warn("нет доступного текущего вопроса для текстового ответа", "student_id", student.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Сейчас нет открытого текстового вопроса. Отправьте /start и выберите курс и урок.")
		return !isRetryableUpdateError(err)
	}
	if state.Question.AnswerType != "text" {
		b.send(ctx, message.Chat.ID, "Для этого вопроса используйте кнопки вариантов ответа.")
		return true
	}
	if state.Progress.Enrollment.CurrentQuestionID == nil ||
		*state.Progress.Enrollment.CurrentQuestionID != state.Question.ID ||
		state.Progress.Enrollment.Status != "in_progress" {
		b.send(ctx, message.Chat.ID, "Текущий вопрос изменился. Откройте его через кнопки и отправьте ответ ещё раз.")
		return true
	}
	if strings.TrimSpace(text) == "" {
		b.send(ctx, message.Chat.ID, "Пустой ответ не сохранён. Напишите ответ и отправьте его ещё раз.")
		return true
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
		return !isRetryableUpdateError(err)
	}

	// Сохранение уже завершено. Ошибка удаления сообщения Telegram не должна отменять ответ.
	b.deleteIncomingMessage(ctx, message)
	var refreshed service.NavigationState
	var refreshErr error
	if current, ok := b.learning.(interface {
		OpenCurrentQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	}); ok {
		refreshed, refreshErr = current.OpenCurrentQuestion(ctx, student.ID, result.Progress.Enrollment.ID, state.Question.ID)
	} else {
		refreshed, refreshErr = b.learning.OpenQuestion(ctx, student.ID, result.Progress.Enrollment.ID, state.Question.ID)
	}
	if refreshErr != nil {
		state.SavedAnswer = &result.Answer
		state.Progress = result.Progress
	} else {
		state = refreshed
	}
	b.renderQuestion(ctx, message.Chat.ID, student.ID, b.knownMainMessage(message.Chat.ID), state, nil, 0)
	return true
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


func (b *Bot) handleAnswerCallback(ctx context.Context, updateID, chatID, studentID, messageID int64, parts []string, replyMarkup *InlineKeyboardMarkup) bool {
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
	case "pick":
		need = 4
	case "multi-toggle":
		need = 4
		if len(parts) == 5 {
			need = 5
		}
	case "multi-submit":
		need = 3
	default:
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return true
	}
	if len(parts) != need {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return true
	}
	enrollmentID, okEnrollment := parsePositiveID(parts[1])
	questionID, okQuestion := parsePositiveID(parts[2])
	if !okEnrollment || !okQuestion {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return true
	}
	var optionID int64
	if action == "pick" || action == "multi-toggle" {
		var ok bool
		optionID, ok = parsePositiveID(parts[3])
		if !ok {
			b.showStaleCallback(ctx, chatID, studentID, messageID)
			return true
		}
	}
	desiredSelected := false
	if action == "multi-toggle" && len(parts) == 5 {
		if parts[4] != "0" && parts[4] != "1" {
			b.showStaleCallback(ctx, chatID, studentID, messageID)
			return true
		}
		desiredSelected = parts[4] == "1"
	}

	var state service.NavigationState
	var err error
	if current, ok := b.learning.(interface {
		OpenCurrentQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	}); ok {
		state, err = current.OpenCurrentQuestion(ctx, studentID, enrollmentID, questionID)
		if errors.Is(err, domain.ErrConflict) && state.StaleCallback {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
	} else {
		progress, progressErr := b.learning.GetProgress(ctx, studentID, enrollmentID)
		if progressErr != nil || progress.Enrollment.Status != "in_progress" ||
			progress.Enrollment.CurrentQuestionID == nil ||
			*progress.Enrollment.CurrentQuestionID != questionID {
			if progressErr != nil {
				b.showStaleCallback(ctx, chatID, studentID, messageID)
				return !isRetryableUpdateError(progressErr)
			}
			b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID, 0)
			return true
		}
		state, err = b.learning.OpenQuestion(ctx, studentID, enrollmentID, questionID)
	}
	if err != nil {
		b.logger.Warn("не удалось проверить вопрос для ответа кнопкой", "student_id", studentID,
			"enrollment_id", enrollmentID, "question_id", questionID, "error", err)
		b.send(ctx, chatID, "Не удалось проверить текущий вопрос. Попробуйте открыть его ещё раз.")
		return !isRetryableUpdateError(err)
	}
	key := answerStateKey{studentID: studentID, enrollmentID: enrollmentID, questionID: questionID}
	switch action {
	case "pick-edit":
		if state.Question.AnswerType != "single_choice" && state.Question.AnswerType != "multiple_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		b.setChoiceEditing(key, true)
		if state.Question.AnswerType == "multiple_choice" && state.SavedAnswer != nil {
			b.seedMultiDraft(key, state.SavedAnswer.SelectedOptionIDs)
		}
		b.renderQuestionMode(ctx, chatID, studentID, messageID, state, nil, 0, true, "")
		return true
	case "pick":
		if state.Question.AnswerType != "single_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		if state.SavedAnswer != nil && !b.isChoiceEditing(key) {
			b.send(ctx, chatID, "Ответ уже сохранён. Нажмите «Изменить ответ», чтобы выбрать другой вариант.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		if !questionHasOption(state.Question.Options, optionID) {
			b.send(ctx, chatID, "Выбранный вариант не относится к текущему вопросу. Выберите вариант на экране вопроса.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		return b.saveChoiceAnswer(ctx, updateID, chatID, studentID, messageID, state, []int64{optionID})
	case "multi-toggle":
		if state.Question.AnswerType != "multiple_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		if state.SavedAnswer != nil && !b.isChoiceEditing(key) {
			b.send(ctx, chatID, "Ответ уже сохранён. Нажмите «Изменить ответ», чтобы изменить выбор.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		if !questionHasOption(state.Question.Options, optionID) {
			b.send(ctx, chatID, "Выбранный вариант не относится к текущему вопросу.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		if len(parts) == 5 {
			b.seedMultiDraftFromMarkup(key, replyMarkup)
			b.setMultiDraftOption(key, optionID, desiredSelected)
		} else {
			// Compatibility with keyboards rendered before desired-state callbacks.
			b.toggleMultiDraft(key, optionID)
		}
		b.setChoiceEditing(key, true)
		b.renderQuestionMode(ctx, chatID, studentID, messageID, state, nil, 0, true, "")
		return true
	case "multi-submit":
		if state.Question.AnswerType != "multiple_choice" {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		if state.SavedAnswer != nil && !b.isChoiceEditing(key) {
			b.send(ctx, chatID, "Ответ уже сохранён. Нажмите «Изменить ответ», чтобы изменить выбор.")
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
			return true
		}
		selected := orderedDraftSelection(state.Question.Options, b.multiDraftSelection(key))
		if len(selected) == 0 {
			b.send(ctx, chatID, "Сначала отметьте хотя бы один вариант, затем подтвердите выбор.")
			return true
		}
		return b.saveChoiceAnswer(ctx, updateID, chatID, studentID, messageID, state, selected)
	}
	return true
}

func (b *Bot) saveChoiceAnswer(ctx context.Context, updateID, chatID, studentID, messageID int64, state service.NavigationState, selected []int64) bool {
	answerService, ok := b.learning.(answerLearningService)
	if !ok {
		b.send(ctx, chatID, "Не удалось сохранить выбор. Попробуйте ещё раз.")
		return true
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
		return !isRetryableUpdateError(err)
	}
	b.clearChoiceState(answerStateKey{studentID: studentID, enrollmentID: enrollmentID, questionID: questionID})
	var refreshed service.NavigationState
	var refreshErr error
	if current, ok := b.learning.(interface {
		OpenCurrentQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	}); ok {
		refreshed, refreshErr = current.OpenCurrentQuestion(ctx, studentID, enrollmentID, questionID)
	} else {
		refreshed, refreshErr = b.learning.OpenQuestion(ctx, studentID, enrollmentID, questionID)
	}
	if refreshErr != nil {
		state.SavedAnswer = &result.Answer
		state.Progress = result.Progress
	} else {
		state = refreshed
	}
	b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, 0)
	return true
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
