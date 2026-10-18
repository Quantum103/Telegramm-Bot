package telegrambot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type sentMessage struct {
	chatID int64
	text   string
}

type fakeAPI struct {
	getMeErr        error
	deleteWebhookErr error
	sendErr         error
	messages        []sentMessage
	updates         []Update
	updateErr       error
	blockUpdates    bool
	mu              sync.Mutex
}

func (f *fakeAPI) GetMe(context.Context) (User, error) {
	if f.getMeErr != nil {
		return User{}, f.getMeErr
	}
	return User{ID: 321, Username: "study_bot"}, nil
}

func (f *fakeAPI) DeleteWebhook(context.Context) error {
	return f.deleteWebhookErr
}

func (f *fakeAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	if f.blockUpdates {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return nil, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.messages = append(f.messages, sentMessage{chatID: chatID, text: text})
	return nil
}

type fakeStudents struct {
	mu      sync.Mutex
	calls   int
	byID    map[int64]domain.Student
	params  []domain.EnsureStudentParams
	err     error
}

func (f *fakeStudents) EnsureByTelegramID(_ context.Context, params domain.EnsureStudentParams) (domain.Student, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.params = append(f.params, params)
	if f.err != nil {
		return domain.Student{}, f.err
	}
	if f.byID == nil {
		f.byID = make(map[int64]domain.Student)
	}
	if student, exists := f.byID[params.TelegramID]; exists {
		student.DisplayName = params.DisplayName
		f.byID[params.TelegramID] = student
		return student, nil
	}
	student := domain.Student{
		ID:          int64(len(f.byID) + 1),
		TelegramID:  params.TelegramID,
		DisplayName: params.DisplayName,
		AccountStatus: "active",
	}
	f.byID[params.TelegramID] = student
	return student, nil
}

func newTestBot(api *fakeAPI, students *fakeStudents) *Bot {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBot(api, students, logger)
}

func testUpdate(updateID int64, command string, user User) Update {
	return Update{
		UpdateID: updateID,
		Message: &Message{
			From: &user,
			Chat: Chat{ID: user.ID, Type: "private"},
			Text: command,
		},
	}
}

func TestStartRegistersStudentAndSendsGreeting(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	bot := newTestBot(api, students)
	user := User{ID: 987654, FirstName: "Анна", LastName: "Иванова", Username: "anna"}

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))

	if students.calls != 1 {
		t.Fatalf("EnsureByTelegramID called %d times, want 1", students.calls)
	}
	if len(students.params) != 1 {
		t.Fatalf("registration params count = %d, want 1", len(students.params))
	}
	got := students.params[0]
	if got.TelegramID != user.ID || got.DisplayName != "Анна Иванова" {
		t.Errorf("unexpected registration data: %+v", got)
	}
	if got.TelegramUsername == nil || *got.TelegramUsername != "anna" {
		t.Errorf("username was not mapped: %+v", got.TelegramUsername)
	}
	if got.PreferredName == nil || *got.PreferredName != "Анна" {
		t.Errorf("first name was not mapped: %+v", got.PreferredName)
	}
	if len(api.messages) != 1 || !strings.Contains(api.messages[0].text, "Регистрация") {
		t.Fatalf("expected registration greeting, got %+v", api.messages)
	}
	if api.messages[0].chatID != user.ID {
		t.Errorf("reply chat ID = %d, want %d", api.messages[0].chatID, user.ID)
	}
}

func TestRepeatedStartKeepsOneStudentForTelegramID(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	bot := newTestBot(api, students)
	user := User{ID: 987654, FirstName: "Павел"}

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	bot.handleUpdate(context.Background(), testUpdate(2, "/start@platform_bot", user))

	if students.calls != 2 {
		t.Fatalf("expected existing student to be ensured on each /start, calls = %d", students.calls)
	}
	if len(students.byID) != 1 {
		t.Fatalf("duplicate student records in fake store: %d", len(students.byID))
	}
	if len(api.messages) != 2 {
		t.Fatalf("expected a response for both /start commands, got %d", len(api.messages))
	}
}

func TestHelpAndCancelCommands(t *testing.T) {
	api := &fakeAPI{}
	bot := newTestBot(api, &fakeStudents{})
	user := User{ID: 100}

	bot.handleUpdate(context.Background(), testUpdate(1, "/help", user))
	bot.handleUpdate(context.Background(), testUpdate(2, "/cancel", user))

	if len(api.messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(api.messages))
	}
	if !strings.Contains(api.messages[0].text, "/start") || !strings.Contains(api.messages[0].text, "/cancel") {
		t.Errorf("help response lacks command instructions: %q", api.messages[0].text)
	}
	if !strings.Contains(api.messages[1].text, "нет активного действия") {
		t.Errorf("unexpected cancel response: %q", api.messages[1].text)
	}
}

func TestStartRegistrationErrorReturnsSafeMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{err: errors.New("database connection refused")}
	bot := newTestBot(api, students)

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", User{ID: 200, FirstName: "Мария"}))

	if len(api.messages) != 1 {
		t.Fatalf("expected one safe error message, got %d", len(api.messages))
	}
	if strings.Contains(api.messages[0].text, "database") || !strings.Contains(api.messages[0].text, "Попробуйте ещё раз") {
		t.Errorf("database details leaked or safe message missing: %q", api.messages[0].text)
	}
}

func TestInvalidUpdatesAreIgnored(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	bot := newTestBot(api, students)

	bot.handleUpdate(context.Background(), Update{UpdateID: 1})
	bot.handleUpdate(context.Background(), Update{UpdateID: 2, Message: &Message{Chat: Chat{ID: 10, Type: "private"}, Text: "/start"}})
	bot.handleUpdate(context.Background(), testUpdate(3, "/start", User{ID: 10}))

	if students.calls != 1 {
		t.Errorf("registration calls = %d, want 1 for the valid update only", students.calls)
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	api := &fakeAPI{blockUpdates: true}
	bot := newTestBot(api, &fakeStudents())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- bot.Run(ctx)
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error during cancellation: %v", err)
		}
	case <-context.Background().Done():
		t.Fatal("unreachable")
	}
}

func TestRunStopsOnPollingConflict(t *testing.T) {
	api := &fakeAPI{updateErr: &APIError{Code: 409, Description: "Conflict: another getUpdates request"}}
	bot := newTestBot(api, &fakeStudents())

	err := bot.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("Run error = %v, want a fatal 409 polling-conflict error", err)
	}
}
