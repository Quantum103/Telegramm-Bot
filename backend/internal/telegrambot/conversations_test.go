package telegrambot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeStudentConversationService struct {
	chatMode  map[int64]bool
	messages  []domain.CreateStudentConversationMessageParams
	createErr error
	setErr    error
	isModeErr error
	replayed  bool
}

func newFakeStudentConversationService() *fakeStudentConversationService {
	return &fakeStudentConversationService{chatMode: make(map[int64]bool)}
}

func (f *fakeStudentConversationService) CreateStudentMessage(_ context.Context, params domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error) {
	if f.createErr != nil {
		return domain.ConversationMessage{}, f.createErr
	}
	f.messages = append(f.messages, params)
	return domain.ConversationMessage{Replayed: f.replayed}, nil
}

func (f *fakeStudentConversationService) SetStudentChatMode(_ context.Context, studentID int64, enabled bool) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.chatMode[studentID] = enabled
	return nil
}

func (f *fakeStudentConversationService) IsStudentChatMode(_ context.Context, studentID int64) (bool, error) {
	if f.isModeErr != nil {
		return false, f.isModeErr
	}
	return f.chatMode[studentID], nil
}

func TestStudentCanEnterChatModeAndSendMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	conversations := newFakeStudentConversationService()
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, conversations)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	if !hasButton(api.messages[0].markup, "chat:start") {
		t.Fatalf("в меню отсутствует кнопка переписки: %+v", api.messages[0].markup)
	}

	bot.handleCallbackWithUpdateID(context.Background(), 2, testCallback("chat:start", user, api.messages[0].messageID))
	student := students.byID[user.ID]
	if !conversations.chatMode[student.ID] {
		t.Fatal("режим общения не включился")
	}
	if !strings.Contains(lastEdit(t, api).text, "Напишите сообщение") {
		t.Fatalf("не показана инструкция для общения: %q", lastEdit(t, api).text)
	}

	incoming := Update{UpdateID: 3, Message: &Message{
		MessageID: 321, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
		Text: "Нужна помощь с уроком.",
	}}
	if handled := bot.handleUpdate(context.Background(), incoming); !handled {
		t.Fatal("успешно сохранённое сообщение отмечено необработанным")
	}
	if len(conversations.messages) != 1 {
		t.Fatalf("количество сохранённых сообщений = %d, want 1", len(conversations.messages))
	}
	saved := conversations.messages[0]
	if saved.StudentID != student.ID || saved.TelegramChatID != user.ID ||
		saved.TelegramMessageID != 321 || saved.BodyText != "Нужна помощь с уроком." {
		t.Fatalf("неверная идентичность или содержимое сообщения: %+v", saved)
	}
	if !strings.Contains(api.messages[len(api.messages)-1].text, "принято и сохранено") {
		t.Fatalf("нет подтверждения сохранения: %q", api.messages[len(api.messages)-1].text)
	}
}

func TestStudentWithoutCuratorKeepsOriginalMessageVisible(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	conversations := newFakeStudentConversationService()
	conversations.chatMode[1] = true
	conversations.createErr = domain.ErrInvalidState
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, conversations)
	user := testUser()

	update := Update{UpdateID: 10, Message: &Message{
		MessageID: 777, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
		Text: "Не отправляйте это сообщение без назначения.",
	}}
	if handled := bot.handleUpdate(context.Background(), update); !handled {
		t.Fatal("отсутствие назначения не должно бесконечно повторять update")
	}
	if len(conversations.messages) != 0 {
		t.Fatalf("сообщение без куратора неожиданно сохранено: %+v", conversations.messages)
	}
	last := api.messages[len(api.messages)-1].text
	if !strings.Contains(last, "НЕ отправлено") || !strings.Contains(last, "скопируйте его") {
		t.Fatalf("бот не объяснил, что текст не отправлен и остаётся в истории: %q", last)
	}
}

func TestChatModePersistsUntilCancelAndDoesNotConsumeLessonTextAfterCancel(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	conversations := newFakeStudentConversationService()
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, conversations)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/chat", user))
	student := students.byID[user.ID]
	if !conversations.chatMode[student.ID] {
		t.Fatal("/chat не включил режим общения")
	}
	bot.handleUpdate(context.Background(), testUpdate(2, "/cancel", user))
	if conversations.chatMode[student.ID] {
		t.Fatal("/cancel не выключил режим общения")
	}
	bot.handleUpdate(context.Background(), Update{
		UpdateID: 3,
		Message: &Message{
			MessageID: 322, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
			Text: "Это должен быть учебный ответ.",
		},
	})
	if len(conversations.messages) != 0 {
		t.Fatalf("обычный текст без режима общения попал в переписку: %+v", conversations.messages)
	}
	if !strings.Contains(api.messages[len(api.messages)-1].text, "текущий вопрос") {
		t.Fatalf("обычный текст не попал в прежний обработчик учебных ответов: %q", api.messages[len(api.messages)-1].text)
	}
}

func TestTemporaryConversationSaveErrorDoesNotAdvanceUpdate(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	conversations := newFakeStudentConversationService()
	conversations.chatMode[1] = true
	conversations.createErr = errors.New("database unavailable")
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, conversations)
	user := testUser()

	handled := bot.handleUpdate(context.Background(), Update{
		UpdateID: 4,
		Message: &Message{
			MessageID: 323, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
			Text: "Пожалуйста, повторите сохранение.",
		},
	})
	if handled {
		t.Fatal("временная ошибка PostgreSQL должна оставить Telegram update неподтверждённым")
	}
	if len(conversations.messages) != 0 {
		t.Fatal("сообщение не должно считаться сохранённым")
	}
}
