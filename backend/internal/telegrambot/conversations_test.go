package telegrambot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeStudentConversationService struct {
	chatMode           map[int64]bool
	lessonMode         map[int64]bool
	messages           []domain.CreateStudentConversationMessageParams
	createErr          error
	setErr             error
	isModeErr          error
	replayed           bool
	leaveCalls         int
	chatModeOffCalls   int
	lessonModeOffCalls int
}

func newFakeStudentConversationService() *fakeStudentConversationService {
	return &fakeStudentConversationService{chatMode: make(map[int64]bool), lessonMode: make(map[int64]bool)}
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
	if !enabled {
		f.chatModeOffCalls++
	}
	f.chatMode[studentID] = enabled
	if enabled {
		f.lessonMode[studentID] = false
	}
	return nil
}

func (f *fakeStudentConversationService) SetStudentLessonMode(_ context.Context, studentID int64, enabled bool) error {
	if f.setErr != nil {
		return f.setErr
	}
	if !enabled {
		f.lessonModeOffCalls++
	}
	f.lessonMode[studentID] = enabled
	if enabled {
		f.chatMode[studentID] = false
	}
	return nil
}

func (f *fakeStudentConversationService) LeaveStudentInteraction(_ context.Context, studentID int64) error {
	f.leaveCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.chatMode[studentID] = false
	f.lessonMode[studentID] = false
	return nil
}

func (f *fakeStudentConversationService) IsStudentChatMode(_ context.Context, studentID int64) (bool, error) {
	if f.isModeErr != nil {
		return false, f.isModeErr
	}
	return f.chatMode[studentID], nil
}

type fakeStaffTelegramAPIForBinding struct {
	account       domain.StaffTelegramAccount
	findErr       error
	bindCalls     int
	boundTelegram int64
	boundToken    string
	restoreCalls  int
	bindErr       error
}

func (f *fakeStaffTelegramAPIForBinding) FindByTelegramID(context.Context, int64) (domain.StaffTelegramAccount, error) {
	return f.account, f.findErr
}

func (f *fakeStaffTelegramAPIForBinding) BindTelegramAccount(_ context.Context, telegramID int64, _ string, token string) (domain.StaffTelegramAccount, error) {
	f.bindCalls++
	f.boundTelegram = telegramID
	f.boundToken = token
	if f.bindErr != nil {
		return domain.StaffTelegramAccount{}, f.bindErr
	}
	return f.account, nil
}

func (f *fakeStaffTelegramAPIForBinding) RestoreNotifications(context.Context, int64) error {
	f.restoreCalls++
	return nil
}

func TestLinkedCuratorCanUseLinkCommandForConfirmedRebinding(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	staffTelegram := &fakeStaffTelegramAPIForBinding{account: domain.StaffTelegramAccount{
		StaffID: 77, DisplayName: "Куратор", AccountStatus: "active", IsCurator: true,
	}}
	bot := NewBotWithChapterPublisherAndConversationsAndStaffTelegram(
		api, students, catalog, learning, nil, nil, nil, staffTelegram,
	)
	user := User{ID: 987654, Username: "curator"}
	update := Update{UpdateID: 1, Message: &Message{
		MessageID: 5, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
		Text: "/link one-time-rebinding-token",
	}}
	if !bot.handleUpdate(context.Background(), update) {
		t.Fatal("команда привязки не должна блокировать Telegram update")
	}
	if staffTelegram.bindCalls != 1 || staffTelegram.boundTelegram != user.ID ||
		staffTelegram.boundToken != "one-time-rebinding-token" {
		t.Fatalf("повторная привязка существующего куратора не дошла до проверки токена: %+v", staffTelegram)
	}
	if len(api.messages) == 0 || !strings.Contains(api.messages[len(api.messages)-1].text, "успешно привязан") {
		t.Fatalf("бот не подтвердил результат привязки: %+v", api.messages)
	}
}

func TestLinkedAdministratorCanRunStartWithoutCuratorRole(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	staffTelegram := &fakeStaffTelegramAPIForBinding{account: domain.StaffTelegramAccount{
		StaffID: 78, DisplayName: "Администратор", AccountStatus: "active", IsAdministrator: true,
	}}
	bot := NewBotWithChapterPublisherAndConversationsAndStaffTelegram(
		api, students, catalog, learning, nil, nil, nil, staffTelegram,
	)
	user := User{ID: 987655, Username: "administrator"}
	update := Update{UpdateID: 2, Message: &Message{
		MessageID: 6, From: &user, Chat: Chat{ID: user.ID, Type: "private"}, Text: "/start",
	}}
	if !bot.handleUpdate(context.Background(), update) {
		t.Fatal("администраторский /start не обработан")
	}
	if staffTelegram.restoreCalls != 1 {
		t.Fatalf("для администратора должен вызываться restore уведомлений, calls=%d", staffTelegram.restoreCalls)
	}
	if len(api.messages) == 0 ||
		!strings.Contains(api.messages[len(api.messages)-1].text, "администратора") ||
		!strings.Contains(api.messages[len(api.messages)-1].text, "эскалац") {
		t.Fatalf("бот не сообщил администратору о привязке и эскалациях: %+v", api.messages)
	}
}

func TestLinkedCuratorStartKeepsCuratorMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	staffTelegram := &fakeStaffTelegramAPIForBinding{account: domain.StaffTelegramAccount{
		StaffID: 79, DisplayName: "Куратор", AccountStatus: "active", IsCurator: true,
	}}
	bot := NewBotWithChapterPublisherAndConversationsAndStaffTelegram(
		api, students, catalog, learning, nil, nil, nil, staffTelegram,
	)
	user := User{ID: 987656, Username: "curator"}
	if !bot.handleUpdate(context.Background(), Update{UpdateID: 4, Message: &Message{
		MessageID: 7, From: &user, Chat: Chat{ID: user.ID, Type: "private"}, Text: "/start",
	}}) {
		t.Fatal("кураторский /start не обработан")
	}
	if staffTelegram.restoreCalls != 1 || len(api.messages) == 0 ||
		!strings.Contains(api.messages[len(api.messages)-1].text, "учётной записи куратора") {
		t.Fatalf("куратор должен получить своё сообщение и восстановление уведомлений: calls=%d messages=%+v", staffTelegram.restoreCalls, api.messages)
	}
}

func TestLinkedAdministratorCanBindTelegramWithOneTimeCode(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	staffTelegram := &fakeStaffTelegramAPIForBinding{account: domain.StaffTelegramAccount{
		StaffID: 80, DisplayName: "Администратор", AccountStatus: "active", IsAdministrator: true,
	}}
	bot := NewBotWithChapterPublisherAndConversationsAndStaffTelegram(
		api, students, catalog, learning, nil, nil, nil, staffTelegram,
	)
	user := User{ID: 987657, Username: "administrator"}
	if !bot.handleUpdate(context.Background(), Update{UpdateID: 4, Message: &Message{
		MessageID: 8, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
		Text: "/link administrator-one-time-code",
	}}) {
		t.Fatal("администраторский /link не обработан")
	}
	if staffTelegram.bindCalls != 1 || staffTelegram.boundTelegram != user.ID ||
		staffTelegram.boundToken != "administrator-one-time-code" ||
		len(api.messages) == 0 || !strings.Contains(api.messages[len(api.messages)-1].text, "успешно привязан") {
		t.Fatalf("администратор не смог безопасно пройти одноразовую привязку: bind=%+v messages=%+v", staffTelegram, api.messages)
	}
}

func TestLinkedStaffWithoutActiveRoleCannotBindTelegram(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	staffTelegram := &fakeStaffTelegramAPIForBinding{bindErr: domain.ErrInvalidState}
	bot := NewBotWithChapterPublisherAndConversationsAndStaffTelegram(
		api, students, catalog, learning, nil, nil, nil, staffTelegram,
	)
	user := User{ID: 987658, Username: "inactive"}
	if !bot.handleUpdate(context.Background(), Update{UpdateID: 5, Message: &Message{
		MessageID: 9, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
		Text: "/link invalid-role-code",
	}}) {
		t.Fatal("ошибка роли не должна блокировать Telegram update")
	}
	if staffTelegram.bindCalls != 1 || len(api.messages) == 0 ||
		!strings.Contains(api.messages[len(api.messages)-1].text, "активной роли") {
		t.Fatalf("привязка без активной роли не была отклонена: %+v", api.messages)
	}
}

func TestStudentCanEnterChatModeAndSendMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	conversations := newFakeStudentConversationService()
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, conversations)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	if len(api.messages) != 1 || !hasButton(api.messages[0].markup, "home:lessons") ||
		hasButton(api.messages[0].markup, "chat:start") {
		t.Fatalf("главное меню должно содержать только две основные кнопки: %+v", api.messages[0].markup)
	}
	bot.handleCallbackWithUpdateID(context.Background(), 2, testCallback("home:lessons", user, api.messages[0].messageID))
	learningMenu := lastEdit(t, api)
	if !hasButton(learningMenu.markup, "chat:start") {
		t.Fatalf("в разделе обучения отсутствует связь с координатором: %+v", learningMenu.markup)
	}

	bot.handleCallbackWithUpdateID(context.Background(), 3, testCallback("chat:start", user, api.messages[0].messageID))
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
	if !strings.Contains(api.messages[len(api.messages)-1].text, "Сообщение сохранено") ||
		!strings.Contains(api.messages[len(api.messages)-1].text, "обязательно вам ответит") {
		t.Fatalf("бот должен подтвердить сохранение и сообщить, что куратор ответит: %q", api.messages[len(api.messages)-1].text)
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

func TestCuratorChatReturnsToCoursesAndLessonDeliveryFollowsCurrentScreen(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	conversations := newFakeStudentConversationService()
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, conversations)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	student := students.byID[user.ID]
	messageID := api.messages[0].messageID
	leaveCallsBefore := conversations.leaveCalls

	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))
	if !conversations.lessonMode[student.ID] {
		t.Fatal("экран вопроса должен включать режим прохождения урока")
	}

	bot.handleCallback(context.Background(), testCallback("chat:start", user, messageID))
	last := lastEdit(t, api)
	if !conversations.chatMode[student.ID] || conversations.lessonMode[student.ID] {
		t.Fatalf("при переходе в чат режим урока должен выключаться: chat=%v lesson=%v",
			conversations.chatMode[student.ID], conversations.lessonMode[student.ID])
	}
	foundReturnButton := false
	for _, row := range last.markup.InlineKeyboard {
		for _, item := range row {
			if item.CallbackData == "chat:cancel" && item.Text == "Вернуться к прохождению курсов" {
				foundReturnButton = true
			}
		}
	}
	if !foundReturnButton {
		t.Fatalf("на экране чата должна быть кнопка возврата к прохождению курсов: %+v", last.markup)
	}

	chatOffBeforeReturn := conversations.chatModeOffCalls
	lessonOffBeforeReturn := conversations.lessonModeOffCalls
	bot.handleCallback(context.Background(), testCallback("chat:cancel", user, messageID))
	if conversations.leaveCalls != leaveCallsBefore+1 {
		t.Fatalf("возврат из чата должен выполнить одну объединённую очистку режимов: calls=%d before=%d", conversations.leaveCalls, leaveCallsBefore)
	}
	if conversations.chatModeOffCalls != chatOffBeforeReturn || conversations.lessonModeOffCalls != lessonOffBeforeReturn {
		t.Fatalf("возврат из чата использовал отдельные операции выключения: chat_off=%d lesson_off=%d",
			conversations.chatModeOffCalls-chatOffBeforeReturn, conversations.lessonModeOffCalls-lessonOffBeforeReturn)
	}
	if conversations.chatMode[student.ID] || conversations.lessonMode[student.ID] {
		t.Fatalf("возврат из чата должен закрыть оба режима: chat=%v lesson=%v",
			conversations.chatMode[student.ID], conversations.lessonMode[student.ID])
	}
	if !strings.Contains(lastEdit(t, api).text, "Выберите курс") {
		t.Fatalf("после возврата должен открыться каталог курсов: %q", lastEdit(t, api).text)
	}

	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))
	if !conversations.lessonMode[student.ID] {
		t.Fatal("повторное открытие урока должно снова включать режим прохождения")
	}
	chatOffBeforeCatalog := conversations.chatModeOffCalls
	lessonOffBeforeCatalog := conversations.lessonModeOffCalls
	bot.handleCallback(context.Background(), testCallback("courses", user, messageID))
	if conversations.lessonMode[student.ID] {
		t.Fatal("переход к курсам должен выключать режим прохождения даже при незавершённом enrollment")
	}
	if conversations.leaveCalls != leaveCallsBefore+2 {
		t.Fatalf("каждый переход в каталог должен использовать одну объединённую операцию: calls=%d before=%d", conversations.leaveCalls, leaveCallsBefore)
	}
	if conversations.chatModeOffCalls != chatOffBeforeCatalog || conversations.lessonModeOffCalls != lessonOffBeforeCatalog {
		t.Fatalf("переход к каталогу использовал отдельные операции выключения: chat_off=%d lesson_off=%d",
			conversations.chatModeOffCalls-chatOffBeforeCatalog, conversations.lessonModeOffCalls-lessonOffBeforeCatalog)
	}
}

func TestReplayedStaffLinkAfterCommittedBindingIsReportedAsAlreadyLinked(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	staffTelegram := &fakeStaffTelegramAPIForBinding{
		account: domain.StaffTelegramAccount{
			StaffID: 78, DisplayName: "Уже привязанный куратор",
			AccountStatus: "active", IsCurator: true,
		},
		bindErr: domain.ErrNotFound, // the one-time token was consumed by the first delivery
	}
	bot := NewBotWithChapterPublisherAndConversationsAndStaffTelegram(
		api, students, catalog, learning, nil, nil, nil, staffTelegram,
	)
	user := User{ID: 987655, Username: "curator"}
	update := Update{UpdateID: 2, Message: &Message{
		MessageID: 6, From: &user, Chat: Chat{ID: user.ID, Type: "private"},
		Text: "/link consumed-one-time-token",
	}}

	if handled := bot.handleUpdate(context.Background(), update); !handled {
		t.Fatal("a replay of an already committed staff binding must be terminal")
	}
	if staffTelegram.bindCalls != 1 {
		t.Fatalf("binding attempts = %d, want exactly one attempt for this delivery", staffTelegram.bindCalls)
	}
	if len(api.messages) == 0 || !strings.Contains(api.messages[len(api.messages)-1].text, "уже привязан") {
		t.Fatalf("bot should report the committed binding instead of an expired token: %+v", api.messages)
	}
}
