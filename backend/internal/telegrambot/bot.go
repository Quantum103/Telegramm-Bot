package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

const (
	apiBaseURL       = "https://api.telegram.org"
	longPollTimeout  = 30
	httpRequestLimit = 4 << 20
)

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type TelegramAPI interface {
	GetMe(context.Context) (User, error)
	DeleteWebhook(context.Context) error
	GetUpdates(context.Context, int64, int) ([]Update, error)
	SendMessage(context.Context, int64, string) error
	SendMessageWithKeyboard(context.Context, int64, string, *InlineKeyboardMarkup) (int64, error)
	EditMessageText(context.Context, int64, int64, string, *InlineKeyboardMarkup) error
	AnswerCallbackQuery(context.Context, string, string) error
}

type StudentRegistrar interface {
	EnsureByTelegramID(context.Context, domain.EnsureStudentParams) (domain.Student, error)
}

type CourseCatalog interface {
	ListCourses(context.Context) ([]domain.Course, error)
	GetCourse(context.Context, int64) (domain.Course, error)
	ListCourseLessons(context.Context, int64) ([]domain.Lesson, error)
	GetLessonDetailInCourse(context.Context, int64, int64) (domain.LessonDetail, error)
	GetLessonDetail(context.Context, int64) (domain.LessonDetail, error)
}

type LearningNavigator interface {
	StartLesson(context.Context, int64, int64, string) (domain.Progress, error)
	GetProgress(context.Context, int64, int64) (domain.Progress, error)
	OpenQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	PreviousQuestion(context.Context, int64, int64) (service.NavigationState, error)
	NextQuestion(context.Context, int64, int64) (service.NavigationState, error)
}

type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("Telegram Bot API вернул ошибку %d", e.Code)
	}
	return fmt.Sprintf("Telegram Bot API, ошибка %d: %s", e.Code, e.Description)
}

type HTTPClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

func NewHTTPClient(token string) *HTTPClient {
	return newHTTPClient(strings.TrimSpace(token), apiBaseURL, &http.Client{Timeout: 40 * time.Second})
}

func newHTTPClient(token, baseURL string, client *http.Client) *HTTPClient {
	return &HTTPClient{token: token, baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

func (c *HTTPClient) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.call(ctx, "getMe", map[string]any{}, &user)
	return user, err
}

func (c *HTTPClient) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (c *HTTPClient) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var updates []Update
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}
	err := c.call(ctx, "getUpdates", payload, &updates)
	return updates, err
}

func (c *HTTPClient) SendMessage(ctx context.Context, chatID int64, text string) error {
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    text,
	}, nil)
}

// htmlTelegramAPI is implemented by clients that can render formatted Telegram messages.
type htmlTelegramAPI interface {
	SendHTMLMessageWithKeyboard(context.Context, int64, string, *InlineKeyboardMarkup) (int64, error)
	EditHTMLMessageText(context.Context, int64, int64, string, *InlineKeyboardMarkup) error
}

func (c *HTTPClient) SendMessageWithKeyboard(ctx context.Context, chatID int64, text string, replyMarkup *InlineKeyboardMarkup) (int64, error) {
	return c.sendMessageWithKeyboard(ctx, chatID, text, replyMarkup, "")
}

func (c *HTTPClient) SendHTMLMessageWithKeyboard(ctx context.Context, chatID int64, text string, replyMarkup *InlineKeyboardMarkup) (int64, error) {
	return c.sendMessageWithKeyboard(ctx, chatID, text, replyMarkup, "HTML")
}

func (c *HTTPClient) sendMessageWithKeyboard(ctx context.Context, chatID int64, text string, replyMarkup *InlineKeyboardMarkup, parseMode string) (int64, error) {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
		if parseMode == "HTML" {
			payload["link_preview_options"] = map[string]bool{"is_disabled": true}
		}
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.call(ctx, "sendMessage", payload, &sent); err != nil {
		return 0, err
	}
	return sent.MessageID, nil
}

func (c *HTTPClient) EditMessageText(ctx context.Context, chatID, messageID int64, text string, replyMarkup *InlineKeyboardMarkup) error {
	return c.editMessageText(ctx, chatID, messageID, text, replyMarkup, "")
}

func (c *HTTPClient) EditHTMLMessageText(ctx context.Context, chatID, messageID int64, text string, replyMarkup *InlineKeyboardMarkup) error {
	return c.editMessageText(ctx, chatID, messageID, text, replyMarkup, "HTML")
}

func (c *HTTPClient) editMessageText(ctx context.Context, chatID, messageID int64, text string, replyMarkup *InlineKeyboardMarkup, parseMode string) error {
	payload := map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
		if parseMode == "HTML" {
			payload["link_preview_options"] = map[string]bool{"is_disabled": true}
		}
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	return c.call(ctx, "editMessageText", payload, nil)
}

func (c *HTTPClient) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

func (c *HTTPClient) AnswerCallbackQuery(ctx context.Context, callbackQueryID, text string) error {
	payload := map[string]any{"callback_query_id": callbackQueryID}
	if text != "" {
		payload["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", payload, nil)
}

func (c *HTTPClient) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("не удалось подготовить запрос к Telegram Bot API")
	}

	endpoint := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("не удалось создать запрос к Telegram Bot API")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Ошибка net/http может включать URL с токеном, поэтому исходная ошибка не возвращается.
		return errors.New("сетевой запрос к Telegram Bot API завершился ошибкой")
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, httpRequestLimit))
	if err != nil {
		return errors.New("не удалось прочитать ответ Telegram Bot API")
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return fmt.Errorf("Telegram Bot API вернул некорректный ответ (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{
			Code: code, Description: envelope.Description,
			RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second,
		}
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return errors.New("не удалось разобрать результат Telegram Bot API")
		}
	}
	return nil
}

type StudentConversationAPI interface {
	CreateStudentMessage(context.Context, domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error)
	SetStudentChatMode(context.Context, int64, bool) error
	IsStudentChatMode(context.Context, int64) (bool, error)
}

type StaffTelegramBotAPI interface {
	BindTelegramAccount(context.Context, int64, string, string) (domain.StaffTelegramAccount, error)
	FindByTelegramID(context.Context, int64) (domain.StaffTelegramAccount, error)
	RestoreNotifications(context.Context, int64) error
}

type Bot struct {
	api              TelegramAPI
	students         StudentRegistrar
	catalog          CourseCatalog
	learning         LearningNavigator
	logger           *slog.Logger
	chapterPublisher ChapterPagePublisher
	conversations    StudentConversationAPI
	staffTelegram    StaffTelegramBotAPI

	// ID основного сообщения — только кэш Telegram UI, не источник состояния обучения.
	messageMu     sync.RWMutex
	mainMessages  map[int64]int64
	choiceMu      sync.Mutex
	choiceEditing map[answerStateKey]bool
	multiDrafts   map[answerStateKey]map[int64]struct{}
}

func NewBot(api TelegramAPI, students StudentRegistrar, catalog CourseCatalog, learning LearningNavigator, logger *slog.Logger) *Bot {
	return NewBotWithChapterPublisher(api, students, catalog, learning, logger, nil)
}

// NewBotWithChapterPublisher keeps the original constructor contract.
func NewBotWithChapterPublisher(api TelegramAPI, students StudentRegistrar, catalog CourseCatalog, learning LearningNavigator, logger *slog.Logger, publisher ChapterPagePublisher) *Bot {
	return NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, logger, publisher, nil)
}

// NewBotWithChapterPublisherAndConversations reuses the current Telegram Bot API client.
func NewBotWithChapterPublisherAndConversations(api TelegramAPI, students StudentRegistrar, catalog CourseCatalog, learning LearningNavigator, logger *slog.Logger, publisher ChapterPagePublisher, conversations StudentConversationAPI) *Bot {
	return NewBotWithChapterPublisherAndConversationsAndStaffTelegram(api, students, catalog, learning, logger, publisher, conversations, nil)
}

// NewBotWithChapterPublisherAndConversationsAndStaffTelegram adds verified curator account binding.
func NewBotWithChapterPublisherAndConversationsAndStaffTelegram(api TelegramAPI, students StudentRegistrar, catalog CourseCatalog, learning LearningNavigator, logger *slog.Logger, publisher ChapterPagePublisher, conversations StudentConversationAPI, staffTelegram StaffTelegramBotAPI) *Bot {
	if logger == nil { logger = slog.Default() }
	return &Bot{
		api: api, students: students, catalog: catalog, learning: learning, logger: logger,
		chapterPublisher: publisher, conversations: conversations, staffTelegram: staffTelegram,
		mainMessages: make(map[int64]int64), choiceEditing: make(map[answerStateKey]bool),
		multiDrafts: make(map[answerStateKey]map[int64]struct{}),
	}
}
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.waitForAPI(ctx)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	b.logger.Info("Telegram-бот авторизован", "telegram_bot_id", me.ID, "username", me.Username)

	var offset int64
	backoff := time.Second
	for {
		updates, err := b.api.GetUpdates(ctx, offset, longPollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isFatalAPIError(err) {
				return fmt.Errorf("long polling Telegram остановлен: %w", err)
			}
			b.logger.Warn("ошибка получения обновлений Telegram; будет повторная попытка", "error", err)
			if !wait(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = time.Second
		for _, update := range updates {
			if update.UpdateID < offset {
				continue
			}
			if !b.handleUpdate(ctx, update) {
				// Не подтверждаем offset при временной ошибке сохранения: Telegram повторит update.
				if !wait(ctx, time.Second) {
					return nil
				}
				break
			}
			offset = update.UpdateID + 1
		}
	}
}

func (b *Bot) waitForAPI(ctx context.Context) (User, error) {
	backoff := time.Second
	var me User
	for {
		if ctx.Err() != nil {
			return User{}, nil
		}
		user, err := b.api.GetMe(ctx)
		if err == nil {
			me = user
			break
		}
		if isFatalAPIError(err) {
			return User{}, fmt.Errorf("проверка токена Telegram не пройдена: %w", err)
		}
		b.logger.Warn("Telegram API временно недоступен при запуске; будет повторная попытка", "error", err)
		if !wait(ctx, backoff) {
			return User{}, nil
		}
		backoff = nextBackoff(backoff)
	}

	backoff = time.Second
	for {
		err := b.api.DeleteWebhook(ctx)
		if err == nil {
			return me, nil
		}
		if ctx.Err() != nil {
			return User{}, nil
		}
		if isFatalAPIError(err) {
			return User{}, fmt.Errorf("не удалось подготовить long polling Telegram: %w", err)
		}
		b.logger.Warn("не удалось подготовить long polling Telegram; будет повторная попытка", "error", err)
		if !wait(ctx, backoff) {
			return User{}, nil
		}
		backoff = nextBackoff(backoff)
	}
}

func (b *Bot) handleUpdate(ctx context.Context, update Update) bool {
	if update.CallbackQuery != nil {
		b.handleCallbackWithUpdateID(ctx, update.UpdateID, *update.CallbackQuery)
		return true
	}
	message := update.Message
	if message == nil || message.From == nil || message.From.ID <= 0 || message.Chat.ID <= 0 {
		return true
	}
	if message.Chat.Type != "" && message.Chat.Type != "private" {
		return true
	}
	if handled, ok := b.handleLinkedStaffUpdate(ctx, *message); !ok {
		return false
	} else if handled {
		return true
	}

	trimmedText := strings.TrimSpace(message.Text)
	if trimmedText == "" {
		return true
	}
	fields := strings.Fields(trimmedText)
	if len(fields) == 0 {
		return true
	}
	if !strings.HasPrefix(fields[0], "/") {
		if b.conversations != nil {
			student, err := b.ensureStudent(ctx, *message.From)
			if err != nil {
				b.logger.Warn("не удалось определить ученика для сообщения куратору", "telegram_user_id", message.From.ID, "error", err)
				return false
			}
			chatMode, err := b.conversations.IsStudentChatMode(ctx, student.ID)
			if err != nil {
				b.logger.Warn("не удалось проверить режим общения ученика", "student_id", student.ID, "error", err)
				return false
			}
			if chatMode {
				return b.handleStudentConversationText(ctx, student, *message, trimmedText)
			}
		}
		b.handleTextAnswer(ctx, update.UpdateID, *message.From, *message, trimmedText)
		return true
	}

	command := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	switch command {
	case "/link":
		b.handleStaffLinkCommand(ctx, message.Chat.ID, *message.From, fields)
	case "/start":
		b.handleStart(ctx, message.Chat.ID, *message.From)
	case "/chat":
		b.handleChatCommand(ctx, message.Chat.ID, *message.From)
	case "/link":
		b.handleStaffLinkCommand(ctx, message.Chat.ID, *message.From, fields)
	case "/help":
		b.send(ctx, message.Chat.ID, "Этот бот предназначен для изучения Библии через уроки и общения с куратором.\n\n/start — открыть каталог курсов\n/chat — написать куратору\n/link КОД — привязать Telegram к учётной записи куратора\n/help — показать эту справку\n/cancel — отменить текущее действие.\n\nВ текстовом вопросе отправьте ответ следующим сообщением. Для общения с куратором сначала включите режим через /chat или меню.")
	case "/cancel":
		b.handleCancelCommand(ctx, message.Chat.ID, *message.From)
	default:
		b.send(ctx, message.Chat.ID, "Пока доступны команды /start, /chat, /help и /cancel.")
	}
	return true
}

func (b *Bot) handleLinkedStaffUpdate(ctx context.Context, message Message) (bool, bool) {
	if b.staffTelegram == nil || message.From == nil { return false, true }
	account, err := b.staffTelegram.FindByTelegramID(ctx, message.From.ID)
	if errors.Is(err, domain.ErrNotFound) { return false, true }
	if err != nil {
		b.logger.Error("не удалось проверить Telegram-привязку сотрудника", "telegram_user_id", message.From.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Не удалось проверить учётную запись. Повторите сообщение позже.")
		return true, false
	}
	if account.AccountStatus != "active" {
		b.send(ctx, message.Chat.ID, "Эта учётная запись сотрудника отключена. Уведомления не отправляются; обратитесь к администратору.")
		return true, true
	}
	if !account.IsCurator {
		b.send(ctx, message.Chat.ID, "У этой учётной записи больше нет активной роли куратора. Уведомления отключены; обратитесь к администратору.")
		return true, true
	}
	fields := strings.Fields(strings.TrimSpace(message.Text))
	command := ""
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") { command = strings.ToLower(strings.SplitN(fields[0], "@", 2)[0]) }
	if command == "/start" {
		if err := b.staffTelegram.RestoreNotifications(ctx, account.StaffID); err != nil {
			b.logger.Error("не удалось восстановить уведомления куратора", "staff_id", account.StaffID, "error", err)
			b.send(ctx, message.Chat.ID, "Привязка активна, но не удалось восстановить уведомления. Повторите /start позже.")
			return true, false
		}
		b.send(ctx, message.Chat.ID, "Telegram привязан к учётной записи куратора. Уведомления включены. Отвечайте ученикам в веб-панели, в разделе «Диалоги».")
	} else {
		b.send(ctx, message.Chat.ID, "Ваша учётная запись привязана как куратор. Просматривайте сообщения и отвечайте ученикам в веб-панели, в разделе «Диалоги».")
	}
	return true, true
}

func (b *Bot) handleStaffLinkCommand(ctx context.Context, chatID int64, user User, fields []string) {
	if b.staffTelegram == nil { b.send(ctx, chatID, "Привязка Telegram сотрудника пока недоступна."); return }
	if chatID != user.ID || len(fields) != 2 {
		b.send(ctx, chatID, "Для привязки откройте личный чат с ботом и отправьте команду /link КОД из веб-панели.")
		return
	}
	account, err := b.staffTelegram.BindTelegramAccount(ctx, user.ID, user.Username, fields[1])
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
		b.send(ctx, chatID, "Код привязки недействителен или истёк. Создайте новый код в веб-панели."); return
	}
	if errors.Is(err, domain.ErrInvalidState) {
		b.send(ctx, chatID, "Привязка недоступна: учётная запись отключена или у неё больше нет роли куратора."); return
	}
	if errors.Is(err, domain.ErrConflict) {
		b.send(ctx, chatID, "Этот Telegram-аккаунт уже привязан к другой учётной записи."); return
	}
	if err != nil {
		b.logger.Error("не удалось привязать Telegram к учётной записи", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось завершить привязку из-за временной ошибки. Создайте новый код позже."); return
	}
	b.send(ctx, chatID, "Telegram успешно привязан к учётной записи «"+account.DisplayName+"». Уведомления о новых сообщениях будут приходить сюда. Отвечайте ученикам в веб-панели.")
}

func (b *Bot) handleChatCommand(ctx context.Context, chatID int64, user User) {
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Warn("не удалось определить ученика для режима общения", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте ещё раз.")
		return
	}
	b.startStudentChat(ctx, chatID, 0, student)
}

func (b *Bot) startStudentChat(ctx context.Context, chatID, messageID int64, student domain.Student) {
	if b.conversations == nil {
		b.render(ctx, chatID, messageID, "Общение с куратором пока недоступно. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return
	}
	if err := b.conversations.SetStudentChatMode(ctx, student.ID, true); err != nil {
		b.logger.Error("не удалось включить режим общения", "student_id", student.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось включить режим общения. Попробуйте ещё раз.", keyboard(button("Повторить", "chat:start")))
		return
	}
	b.render(ctx, chatID, messageID,
		"Режим общения с куратором включён.\n\nНапишите сообщение следующим сообщением. Оно будет сохранено и появится в диалоге вашего назначенного куратора.\n\nЧтобы вернуться к ответам на уроки, нажмите «Отмена» или отправьте /cancel.",
		keyboard(button("Отмена", "chat:cancel")))
}

func (b *Bot) handleCancelCommand(ctx context.Context, chatID int64, user User) {
	if b.conversations == nil {
		b.send(ctx, chatID, "Сейчас нет активного действия для отмены.")
		return
	}
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Warn("не удалось определить ученика при отмене действия", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте снова.")
		return
	}
	enabled, err := b.conversations.IsStudentChatMode(ctx, student.ID)
	if err != nil {
		b.logger.Warn("не удалось проверить режим общения при отмене", "student_id", student.ID, "error", err)
		b.send(ctx, chatID, "Не удалось отменить действие из-за временной ошибки. Попробуйте ещё раз.")
		return
	}
	if !enabled {
		b.send(ctx, chatID, "Сейчас нет активного действия для отмены.")
		return
	}
	if err := b.conversations.SetStudentChatMode(ctx, student.ID, false); err != nil {
		b.logger.Error("не удалось отключить режим общения", "student_id", student.ID, "error", err)
		b.send(ctx, chatID, "Не удалось выключить режим общения. Попробуйте ещё раз.")
		return
	}
	b.send(ctx, chatID, "Режим общения выключен. Следующие текстовые сообщения будут обрабатываться как ответы на вопросы уроков.")
}

func (b *Bot) handleStudentConversationText(ctx context.Context, student domain.Student, message Message, bodyText string) bool {
	saved, err := b.conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: student.ID, TelegramChatID: message.Chat.ID,
		TelegramMessageID: message.MessageID, BodyText: bodyText,
	})
	if errors.Is(err, domain.ErrInvalidState) {
		// Не удаляем текст и не подтверждаем как отправленный: он остаётся в истории Telegram-чата.
		b.send(ctx, message.Chat.ID, "Сейчас вам не назначен активный куратор. Сообщение НЕ отправлено и не сохранено. Оно остаётся в истории этого Telegram-чата: скопируйте его и отправьте повторно после назначения куратора.")
		return true
	}
	if errors.Is(err, domain.ErrInvalidInput) {
		b.send(ctx, message.Chat.ID, "Сообщение не отправлено: допустимая длина — до 4000 символов. Сократите текст и отправьте его ещё раз.")
		return true
	}
	if err != nil {
		b.logger.Error("не удалось сохранить сообщение ученика", "student_id", student.ID, "telegram_message_id", message.MessageID, "error", err)
		return false
	}
	if saved.Replayed {
		b.send(ctx, message.Chat.ID, "Это сообщение уже принято и сохранено. Куратор увидит его в диалоге.")
	} else {
		b.send(ctx, message.Chat.ID, "Сообщение принято и сохранено. Ваш назначенный куратор увидит его в диалоге.")
	}
	return true
}

func (b *Bot) handleStart(ctx context.Context, chatID int64, user User) {
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Error("не удалось зарегистрировать ученика Telegram", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось завершить регистрацию из-за временной ошибки. Попробуйте ещё раз немного позже.")
		return
	}
	if b.conversations != nil {
		if err := b.conversations.SetStudentChatMode(ctx, student.ID, false); err != nil {
			b.logger.Warn("не удалось выключить режим общения при открытии каталога", "student_id", student.ID, "error", err)
		}
	}
	name := student.DisplayName
	if strings.TrimSpace(name) == "" {
		name = displayName(user)
	}
	b.showCourses(ctx, chatID, student.ID, 0, "Привет, "+name+"! Выберите курс для изучения Библии.")
}

func (b *Bot) ensureStudent(ctx context.Context, user User) (domain.Student, error) {
	return b.students.EnsureByTelegramID(ctx, domain.EnsureStudentParams{
		TelegramID: user.ID, DisplayName: displayName(user),
		TelegramUsername: optionalString(user.Username), PreferredName: optionalString(user.FirstName),
	})
}

func (b *Bot) send(ctx context.Context, chatID int64, text string) {
	if err := b.api.SendMessage(ctx, chatID, text); err != nil && ctx.Err() == nil {
		b.logger.Warn("не удалось отправить сообщение Telegram", "chat_id", chatID, "error", err)
	}
}

func displayName(user User) string {
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if name != "" {
		return name
	}
	if username := strings.TrimSpace(user.Username); username != "" {
		return "@" + username
	}
	return "Ученик Telegram"
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func isFatalAPIError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden || apiErr.Code == http.StatusConflict
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current >= 30*time.Second {
		return 30 * time.Second
	}
	next := current * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}
