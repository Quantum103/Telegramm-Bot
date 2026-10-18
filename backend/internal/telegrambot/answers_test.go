package telegrambot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

type answerTestAPI struct {
	*fakeAPI
	deleteErr error
	deleted   []deletedTelegramMessage
}

func (a *answerTestAPI) DeleteMessage(_ context.Context, chatID, messageID int64) error {
	a.deleted = append(a.deleted, deletedTelegramMessage{chatID: chatID, messageID: messageID})
	return a.deleteErr
}

type answerTestLearning struct {
	*fakeLearning
	saved       map[answerStateKey]domain.SavedAnswer
	idempotency map[string]domain.SavedAnswer
	attempts    []domain.AnswerSubmission
	submitErr   error
}

func newAnswerTestLearning(base *fakeLearning) *answerTestLearning {
	return &answerTestLearning{
		fakeLearning: base,
		saved:        make(map[answerStateKey]domain.SavedAnswer),
		idempotency:  make(map[string]domain.SavedAnswer),
	}
}

func (f *answerTestLearning) CurrentQuestion(ctx context.Context, studentID int64) (service.NavigationState, error) {
	f.fakeLearning.mu.Lock()
	var enrollmentID int64
	var selected domain.Progress
	found := false
	for id, p := range f.fakeLearning.progress {
		if p.Enrollment.StudentID == studentID && p.Enrollment.Status == "in_progress" && p.Enrollment.CurrentQuestionID != nil {
			if !found || p.Enrollment.UpdatedAt.After(selected.Enrollment.UpdatedAt) || p.Enrollment.ID > selected.Enrollment.ID {
				enrollmentID, selected, found = id, p, true
			}
		}
	}
	f.fakeLearning.mu.Unlock()
	if !found {
		return service.NavigationState{}, domain.ErrNotFound
	}
	state, err := f.fakeLearning.OpenQuestion(ctx, studentID, enrollmentID, *selected.Enrollment.CurrentQuestionID)
	if err != nil {
		return service.NavigationState{}, err
	}
	f.addSavedAnswer(&state)
	return state, nil
}

func (f *answerTestLearning) OpenQuestion(ctx context.Context, studentID, enrollmentID, questionID int64) (service.NavigationState, error) {
	state, err := f.fakeLearning.OpenQuestion(ctx, studentID, enrollmentID, questionID)
	if err != nil {
		return service.NavigationState{}, err
	}
	f.addSavedAnswer(&state)
	return state, nil
}

func (f *answerTestLearning) addSavedAnswer(state *service.NavigationState) {
	key := answerStateKey{
		studentID:    state.Progress.Enrollment.StudentID,
		enrollmentID: state.Progress.Enrollment.ID,
		questionID:   state.Question.ID,
	}
	if answer, ok := f.saved[key]; ok {
		copy := answer
		state.SavedAnswer = &copy
	}
}

func (f *answerTestLearning) SubmitAnswer(_ context.Context, params domain.AnswerSubmission) (service.SubmissionResult, error) {
	if f.submitErr != nil {
		return service.SubmissionResult{}, f.submitErr
	}
	f.fakeLearning.mu.Lock()
	defer f.fakeLearning.mu.Unlock()

	progress, ok := f.fakeLearning.progress[params.EnrollmentID]
	if !ok || progress.Enrollment.StudentID != params.StudentID ||
		progress.Enrollment.Status != "in_progress" || progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != params.QuestionID {
		return service.SubmissionResult{}, domain.ErrNotFound
	}
	var question *domain.LessonQuestion
	for i := range f.fakeLearning.questions[params.EnrollmentID] {
		if f.fakeLearning.questions[params.EnrollmentID][i].ID == params.QuestionID {
			question = &f.fakeLearning.questions[params.EnrollmentID][i]
			break
		}
	}
	if question == nil {
		return service.SubmissionResult{}, domain.ErrNotFound
	}
	idemKey := ""
	if params.IdempotencyKey != "" {
		idemKey = strings.Join([]string{strings.TrimSpace(time.Unix(0, 0).UTC().Format(time.RFC3339Nano)), params.IdempotencyKey, strconv.FormatInt(params.EnrollmentID, 10)}, "|")
		if previous, exists := f.idempotency[idemKey]; exists {
			previous.Replayed = true
			return service.SubmissionResult{Answer: previous, Progress: progress}, nil
		}
	}
	switch question.AnswerType {
	case "text":
		if strings.TrimSpace(params.AnswerText) == "" || len(params.SelectedOptionIDs) != 0 {
			return service.SubmissionResult{}, domain.ErrInvalidInput
		}
	case "single_choice":
		if strings.TrimSpace(params.AnswerText) != "" || len(params.SelectedOptionIDs) != 1 {
			return service.SubmissionResult{}, domain.ErrInvalidInput
		}
	case "multiple_choice":
		if strings.TrimSpace(params.AnswerText) != "" || len(params.SelectedOptionIDs) == 0 {
			return service.SubmissionResult{}, domain.ErrInvalidInput
		}
	default:
		return service.SubmissionResult{}, domain.ErrInvalidState
	}
	allowed := make(map[int64]struct{}, len(question.Options))
	for _, option := range question.Options {
		allowed[option.ID] = struct{}{}
	}
	seen := make(map[int64]struct{}, len(params.SelectedOptionIDs))
	for _, id := range params.SelectedOptionIDs {
		if _, ok := allowed[id]; !ok {
			return service.SubmissionResult{}, domain.ErrInvalidInput
		}
		if _, ok := seen[id]; ok {
			return service.SubmissionResult{}, domain.ErrInvalidInput
		}
		seen[id] = struct{}{}
	}

	answerKey := answerStateKey{studentID: params.StudentID, enrollmentID: params.EnrollmentID, questionID: params.QuestionID}
	if _, existed := f.saved[answerKey]; !existed {
		progress.AnsweredQuestions++
	}
	progress.Enrollment.Status = "in_progress"
	progress.Enrollment.UpdatedAt = time.Now()
	f.fakeLearning.progress[params.EnrollmentID] = progress
	answer := domain.SavedAnswer{
		AnswerID:          params.EnrollmentID*100000 + params.QuestionID,
		AttemptID:         int64(len(f.attempts) + 1),
		AnswerText:        strings.TrimSpace(params.AnswerText),
		SelectedOptionIDs: append([]int64(nil), params.SelectedOptionIDs...),
		ProcessingStatus:  "pending", UpdatedAt: time.Now(),
	}
	f.attempts = append(f.attempts, params)
	f.saved[answerKey] = answer
	if idemKey != "" {
		f.idempotency[idemKey] = answer
	}
	return service.SubmissionResult{Answer: answer, Progress: progress}, nil
}

func newAnswerTestBot(api *answerTestAPI, students *fakeStudents, catalog *fakeCatalog, learning *answerTestLearning) *Bot {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBot(api, students, catalog, learning, logger)
}

func setupAnswerFlow(t *testing.T, answerType string, options []domain.QuestionOption) (*answerTestAPI, *fakeStudents, *fakeCatalog, *answerTestLearning, *Bot, User, int64) {
	t.Helper()
	api := &answerTestAPI{fakeAPI: &fakeAPI{}}
	students := &fakeStudents{}
	catalog, baseLearning := fixture()
	detail := catalog.details[100]
	detail.Questions[0].AnswerType = answerType
	detail.Questions[0].Options = append([]domain.QuestionOption(nil), options...)
	catalog.details[100] = detail
	learning := newAnswerTestLearning(baseLearning)
	bot := newAnswerTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))
	return api, students, catalog, learning, bot, user, messageID
}

func answerMessage(updateID, messageID int64, user User, text string) Update {
	update := testUpdate(updateID, text, user)
	update.Message.MessageID = messageID
	return update
}

func TestAnswerTextSavedDeletesIncomingAndDoesNotEvaluate(t *testing.T) {
	api, _, _, learning, bot, user, mainID := setupAnswerFlow(t, "text", nil)
	bot.handleUpdate(context.Background(), answerMessage(9001, 811, user, "  ответ о Марке  "))

	if len(learning.attempts) != 1 || learning.attempts[0].AnswerText != "ответ о Марке" {
		t.Fatalf("answer not saved and normalized: %+v", learning.attempts)
	}
	if len(api.deleted) != 1 || api.deleted[0] != (deletedTelegramMessage{chatID: user.ID, messageID: 811}) {
		t.Fatalf("incoming message was not removed after save: %+v", api.deleted)
	}
	last := lastEdit(t, api.fakeAPI)
	if last.messageID != mainID || !strings.Contains(last.text, "ответ о Марке") ||
		!strings.Contains(last.text, "Автоматическая проверка пока не выполнена") {
		t.Fatalf("main screen did not show the saved answer neutrally: %+v", last)
	}
	if strings.Contains(strings.ToLower(last.text), "правильно") || strings.Contains(strings.ToLower(last.text), "неправильно") {
		t.Fatalf("answer was evaluated before stage 5: %q", last.text)
	}
	progress, err := learning.GetProgress(context.Background(), 1, 701)
	if err != nil || progress.AnsweredQuestions != 1 {
		t.Fatalf("progress was not updated: progress=%+v error=%v", progress, err)
	}
}

func TestSameTelegramUpdateIsIdempotent(t *testing.T) {
	api, _, _, learning, bot, user, _ := setupAnswerFlow(t, "text", nil)
	msg := answerMessage(9002, 812, user, "один update")
	bot.handleUpdate(context.Background(), msg)
	bot.handleUpdate(context.Background(), msg)
	if len(learning.attempts) != 1 {
		t.Fatalf("same Telegram update created %d attempts; want 1", len(learning.attempts))
	}
	if len(api.deleted) != 2 {
		t.Fatalf("incoming-message cleanup should be retried idempotently: %+v", api.deleted)
	}
}

func TestChangingTextAnswerCreatesAttemptAndPreservesProgressCount(t *testing.T) {
	_, _, _, learning, bot, user, _ := setupAnswerFlow(t, "text", nil)
	bot.handleUpdate(context.Background(), answerMessage(9003, 813, user, "первая версия"))
	bot.handleUpdate(context.Background(), answerMessage(9004, 814, user, "вторая версия"))
	key := answerStateKey{studentID: 1, enrollmentID: 701, questionID: 1001}
	if len(learning.attempts) != 2 || learning.saved[key].AnswerText != "вторая версия" {
		t.Fatalf("attempt history or latest answer incorrect: attempts=%+v saved=%+v", learning.attempts, learning.saved[key])
	}
	progress, err := learning.GetProgress(context.Background(), 1, 701)
	if err != nil || progress.AnsweredQuestions != 1 {
		t.Fatalf("answer update inflated answered count: %+v err=%v", progress, err)
	}
}

func TestSavedTextAnswerRestoredWhenNavigatingBack(t *testing.T) {
	api, _, _, _, bot, user, mainID := setupAnswerFlow(t, "text", nil)
	bot.handleUpdate(context.Background(), answerMessage(9005, 815, user, "ответ для восстановления"))
	bot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, mainID))
	bot.handleCallback(context.Background(), testCallback("nav:prev:701:1002:10", user, mainID))
	last := lastEdit(t, api.fakeAPI)
	if !strings.Contains(last.text, "ответ для восстановления") {
		t.Fatalf("saved answer did not reappear: %q", last.text)
	}
}

func TestSaveErrorDoesNotDeleteMessageOrLeakDatabaseDetails(t *testing.T) {
	api, _, _, learning, bot, user, _ := setupAnswerFlow(t, "text", nil)
	learning.submitErr = errors.New("postgres private detail")
	bot.handleUpdate(context.Background(), answerMessage(9006, 816, user, "не сохранённый текст"))
	if len(learning.attempts) != 0 || len(api.deleted) != 0 {
		t.Fatalf("failed save should not persist in test store or delete message: attempts=%d deleted=%d", len(learning.attempts), len(api.deleted))
	}
	last := api.messages[len(api.messages)-1]
	if !strings.Contains(last.text, "Не удалось сохранить ответ") || strings.Contains(last.text, "private detail") {
		t.Fatalf("save error was not handled safely: %q", last.text)
	}
}

func TestDeleteFailureDoesNotUndoAnswerSave(t *testing.T) {
	api, _, _, learning, bot, user, _ := setupAnswerFlow(t, "text", nil)
	api.deleteErr = errors.New("Telegram delete failed")
	bot.handleUpdate(context.Background(), answerMessage(9007, 817, user, "ответ в базе"))
	if len(learning.attempts) != 1 || !strings.Contains(lastEdit(t, api.fakeAPI).text, "Ответ сохранён") {
		t.Fatalf("delete failure undid save or success feedback: %+v", learning.attempts)
	}
}

func TestTextWithoutActiveQuestionIsRejected(t *testing.T) {
	api := &answerTestAPI{fakeAPI: &fakeAPI{}}
	students := &fakeStudents{}
	catalog, baseLearning := fixture()
	learning := newAnswerTestLearning(baseLearning)
	bot := newAnswerTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	bot.handleUpdate(context.Background(), answerMessage(9008, 818, user, "просто текст"))
	if len(learning.attempts) != 0 || len(api.deleted) != 0 {
		t.Fatalf("text outside the active question was treated as an answer: attempts=%d deleted=%d", len(learning.attempts), len(api.deleted))
	}
	if !strings.Contains(api.messages[len(api.messages)-1].text, "нет открытого текстового вопроса") {
		t.Fatalf("student was not told how to open a question: %+v", api.messages)
	}
}

func TestSingleChoiceOptionIsSaved(t *testing.T) {
	options := []domain.QuestionOption{{ID: 6101, Key: "a", Text: "первый вариант"}, {ID: 6102, Key: "b", Text: "второй вариант"}}
	api, _, _, learning, bot, user, mainID := setupAnswerFlow(t, "single_choice", options)
	if !hasButton(lastEdit(t, api.fakeAPI).markup, "p:701:1001:6101") {
		t.Fatal("single-choice options were not shown")
	}
	bot.handleCallbackWithUpdateID(context.Background(), 9010, testCallback("p:701:1001:6102", user, mainID))
	if len(learning.attempts) != 1 || len(learning.attempts[0].SelectedOptionIDs) != 1 || learning.attempts[0].SelectedOptionIDs[0] != 6102 {
		t.Fatalf("single-choice answer not saved through the service: %+v", learning.attempts)
	}
	if !strings.Contains(lastEdit(t, api.fakeAPI).text, "второй вариант") ||
		!strings.Contains(lastEdit(t, api.fakeAPI).text, "Автоматическая проверка пока не выполнена") {
		t.Fatalf("saved choice was not rendered neutrally: %q", lastEdit(t, api.fakeAPI).text)
	}
}

func TestOptionBelongingToAnotherQuestionIsRejected(t *testing.T) {
	options := []domain.QuestionOption{{ID: 6201, Key: "a", Text: "только этот вопрос"}}
	api, _, _, learning, bot, user, mainID := setupAnswerFlow(t, "single_choice", options)
	bot.handleCallbackWithUpdateID(context.Background(), 9011, testCallback("p:701:1001:99999", user, mainID))
	if len(learning.attempts) != 0 {
		t.Fatalf("foreign option was saved: %+v", learning.attempts)
	}
	found := false
	for _, message := range api.messages {
		if strings.Contains(message.text, "не относится к текущему вопросу") {
			found = true
		}
	}
	if !found {
		t.Fatal("foreign option rejection wasn't explained")
	}
}

func TestMultipleChoiceNeedsExplicitConfirmation(t *testing.T) {
	options := []domain.QuestionOption{{ID: 6301, Key: "a", Text: "вариант A"}, {ID: 6302, Key: "b", Text: "вариант B"}}
	api, _, _, learning, bot, user, mainID := setupAnswerFlow(t, "multiple_choice", options)
	bot.handleCallbackWithUpdateID(context.Background(), 9020, testCallback("t:701:1001:6301", user, mainID))
	if len(learning.attempts) != 0 {
		t.Fatal("multiple choice saved before confirmation")
	}
	bot.handleCallbackWithUpdateID(context.Background(), 9021, testCallback("t:701:1001:6302", user, mainID))
	bot.handleCallbackWithUpdateID(context.Background(), 9022, testCallback("s:701:1001", user, mainID))
	if len(learning.attempts) != 1 || len(learning.attempts[0].SelectedOptionIDs) != 2 {
		t.Fatalf("confirmed multiple selection wasn't saved: %+v", learning.attempts)
	}
	if !strings.Contains(lastEdit(t, api.fakeAPI).text, "вариант A") || !strings.Contains(lastEdit(t, api.fakeAPI).text, "вариант B") {
		t.Fatalf("saved multiple selection wasn't displayed: %q", lastEdit(t, api.fakeAPI).text)
	}
}

func TestMultipleChoiceDraftsAreIsolatedBetweenStudents(t *testing.T) {
	options := []domain.QuestionOption{{ID: 6401, Key: "a", Text: "общий A"}, {ID: 6402, Key: "b", Text: "общий B"}}
	api := &answerTestAPI{fakeAPI: &fakeAPI{}}
	students := &fakeStudents{}
	catalog, baseLearning := fixture()
	detail := catalog.details[100]
	detail.Questions[0].AnswerType = "multiple_choice"
	detail.Questions[0].Options = options
	catalog.details[100] = detail
	learning := newAnswerTestLearning(baseLearning)
	bot := newAnswerTestBot(api, students, catalog, learning)
	userA := testUser()
	userB := User{ID: 987655, FirstName: "Борис"}
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", userA))
	mainA := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", userA, mainA))
	bot.handleUpdate(context.Background(), testUpdate(2, "/start", userB))
	mainB := api.messages[1].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", userB, mainB))
	bot.handleCallbackWithUpdateID(context.Background(), 9030, testCallback("t:701:1001:6401", userA, mainA))

	var lastB *editedMessage
	for i := range api.edits {
		if api.edits[i].messageID == mainB {
			copy := api.edits[i]
			lastB = &copy
		}
	}
	if lastB == nil {
		t.Fatal("second student's screen wasn't rendered")
	}
	if strings.Contains(lastB.text, "✅ общий A") {
		t.Fatalf("first student's draft leaked into second student's UI: %q", lastB.text)
	}
	if len(learning.attempts) != 0 {
		t.Fatalf("draft choices should not create attempts before confirmation: %+v", learning.attempts)
	}
	if got := len(bot.multiDraftSelection(answerStateKey{studentID: 2, enrollmentID: 702, questionID: 1001})); got != 0 {
		t.Fatalf("second student's draft is not isolated, selected options=%d", got)
	}
}
