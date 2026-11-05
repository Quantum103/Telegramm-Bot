package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

const (
	apiBaseURL               = "https://api.telegram.org"
	longPollTimeout          = 30
	httpRequestLimit         = 4 << 20
	callbackAckCacheCapacity = 4096
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
	MessageID   int64                 `json:"message_id"`
	From        *User                 `json:"from"`
	Chat        Chat                  `json:"chat"`
	Text        string                `json:"text"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
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
	CompleteLesson(context.Context, int64, int64) (domain.Enrollment, error)
}

type APIError struct {
	Code        int
	HTTPStatus  int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("Telegram Bot API вернул ошибку %d", e.Code)
	}
	return fmt.Sprintf("Telegram Bot API, ошибка %d: %s", e.Code, e.Description)
}

type TelegramErrorKind string

const (
	TelegramErrorAPIFailure         TelegramErrorKind = "telegram_api_error"
	TelegramErrorMessageUnchanged   TelegramErrorKind = "message_not_modified"
	TelegramErrorMessageNotFound    TelegramErrorKind = "message_not_found"
	TelegramErrorMessageNotEditable TelegramErrorKind = "message_not_editable"
	TelegramErrorRateLimited        TelegramErrorKind = "rate_limited"
	TelegramErrorServerFailure      TelegramErrorKind = "telegram_server_error"
	TelegramErrorTimeout            TelegramErrorKind = "request_timeout"
	TelegramErrorCancelled          TelegramErrorKind = "request_cancelled"
	TelegramErrorNetwork            TelegramErrorKind = "network_error"
)

// classifyTelegramAPIError provides a stable category for logs and fallback decisions.
// It never includes the bot token or the raw request URL.
func classifyTelegramAPIError(err error) TelegramErrorKind {
	if err == nil {
		return ""
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		description := strings.ToLower(apiErr.Description)
		status := apiErr.HTTPStatus
		if status == 0 {
			status = apiErr.Code
		}
		switch {
		case status == http.StatusBadRequest && strings.Contains(description, "message is not modified"):
			return TelegramErrorMessageUnchanged
		case status == http.StatusBadRequest &&
			(strings.Contains(description, "message to edit not found") ||
				strings.Contains(description, "message not found")):
			return TelegramErrorMessageNotFound
		case status == http.StatusBadRequest &&
			(strings.Contains(description, "message can't be edited") ||
				strings.Contains(description, "message can not be edited")):
			return TelegramErrorMessageNotEditable
		case status == http.StatusTooManyRequests:
			return TelegramErrorRateLimited
		case status >= http.StatusInternalServerError:
			return TelegramErrorServerFailure
		default:
			return TelegramErrorAPIFailure
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TelegramErrorTimeout
	}
	if errors.Is(err, context.Canceled) {
		return TelegramErrorCancelled
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return TelegramErrorTimeout
	}
	return TelegramErrorNetwork
}

// isMessageEditUnavailable is true only for an explicit Telegram rejection proving
// that the target message cannot be edited. Transport and server errors are ambiguous.
func isMessageEditUnavailable(err error) bool {
	kind := classifyTelegramAPIError(err)
	return kind == TelegramErrorMessageNotFound || kind == TelegramErrorMessageNotEditable
}

type HTTPClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

func NewHTTPClient(token string) *HTTPClient {
	// Every Bot API call receives its own context deadline in call(). A zero
	// client timeout preserves the long-polling window and HTTP keep-alive reuse.
	return newHTTPClient(strings.TrimSpace(token), apiBaseURL, &http.Client{})
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

const (
	telegramCallbackTimeout = 3 * time.Second
	telegramRequestTimeout  = 10 * time.Second
	telegramLongPollMargin  = 5 * time.Second
)

func telegramMethodTimeout(method string, payload any) time.Duration {
	switch method {
	case "answerCallbackQuery":
		return telegramCallbackTimeout
	case "getUpdates":
		if values, ok := payload.(map[string]any); ok {
			if seconds, ok := values["timeout"].(int); ok && seconds > 0 {
				return time.Duration(seconds)*time.Second + telegramLongPollMargin
			}
		}
		return telegramRequestTimeout
	default:
		return telegramRequestTimeout
	}
}

func (c *HTTPClient) call(ctx context.Context, method string, payload any, result any) error {
	requestCtx, cancel := context.WithTimeout(ctx, telegramMethodTimeout(method, payload))
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("не удалось подготовить запрос к Telegram Bot API")
	}

	endpoint := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("не удалось создать запрос к Telegram Bot API")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return requestCtx.Err()
		}
		// Ошибка net/http может включать URL с токеном, поэтому исходная ошибка не возвращается.
		return errors.New("сетевой запрос к Telegram Bot API завершился ошибкой")
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, httpRequestLimit))
	if err != nil {
		if requestCtx.Err() != nil {
			return requestCtx.Err()
		}
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
			Code: code, HTTPStatus: resp.StatusCode, Description: envelope.Description,
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
	SetStudentLessonMode(context.Context, int64, bool) error
	IsStudentChatMode(context.Context, int64) (bool, error)
}

type StaffTelegramBotAPI interface {
	BindTelegramAccount(context.Context, int64, string, string) (domain.StaffTelegramAccount, error)
	FindByTelegramID(context.Context, int64) (domain.StaffTelegramAccount, error)
	RestoreNotifications(context.Context, int64) error
}

type StudentReferralAssigner interface {
	AssignStudentByReferral(context.Context, int64, string) (domain.ReferralAssignmentResult, error)
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
	studentReferrals StudentReferralAssigner

	updateWorkerCount   int
	updateQueueCapacity int

	// ID основного сообщения — только кэш Telegram UI, не источник состояния обучения.
	messageMu     sync.RWMutex
	mainMessages  map[int64]int64
	choiceMu      sync.Mutex
	choiceEditing map[answerStateKey]bool
	multiDrafts   map[answerStateKey]map[int64]struct{}

	// Callback acknowledgements are UI-only and deduplicated independently of
	// business update retries. The fixed-size ring bounds memory usage.
	callbackAckMu    sync.Mutex
	callbackAckSeen  map[string]struct{}
	callbackAckOrder []string
	callbackAckNext  int
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
	return NewBotWithChapterPublisherAndConversationsAndStaffTelegramAndStudentReferrals(
		api, students, catalog, learning, logger, publisher, conversations, staffTelegram, nil,
	)
}

// NewBotWithChapterPublisherAndConversationsAndStaffTelegramAndStudentReferrals adds /start referral support.
func NewBotWithChapterPublisherAndConversationsAndStaffTelegramAndStudentReferrals(
	api TelegramAPI,
	students StudentRegistrar,
	catalog CourseCatalog,
	learning LearningNavigator,
	logger *slog.Logger,
	publisher ChapterPagePublisher,
	conversations StudentConversationAPI,
	staffTelegram StaffTelegramBotAPI,
	studentReferrals StudentReferralAssigner,
) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{
		api: api, students: students, catalog: catalog, learning: learning, logger: logger,
		chapterPublisher: publisher, conversations: conversations, staffTelegram: staffTelegram,
		studentReferrals:  studentReferrals,
		updateWorkerCount: defaultUpdateWorkerCount, updateQueueCapacity: defaultUpdateQueueCapacity,
		mainMessages: make(map[int64]int64), choiceEditing: make(map[answerStateKey]bool),
		multiDrafts:      make(map[answerStateKey]map[int64]struct{}),
		callbackAckSeen:  make(map[string]struct{}),
		callbackAckOrder: make([]string, 0, callbackAckCacheCapacity),
	}
}

// ConfigureUpdateWorkerPool sets worker and per-worker queue limits. Configure
// the pool before calling Run; the values are read once when Run starts.
func (b *Bot) ConfigureUpdateWorkerPool(workerCount, queueCapacity int) error {
	if workerCount < 1 || workerCount > 32 {
		return fmt.Errorf("Telegram update worker count must be between 1 and 32")
	}
	if queueCapacity < 1 || queueCapacity > 256 {
		return fmt.Errorf("Telegram update queue capacity must be between 1 and 256")
	}
	b.updateWorkerCount = workerCount
	b.updateQueueCapacity = queueCapacity
	return nil
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

	workerCount := b.updateWorkerCount
	if workerCount < 1 {
		workerCount = defaultUpdateWorkerCount
	}
	queueCapacity := b.updateQueueCapacity
	if queueCapacity < 1 {
		queueCapacity = defaultUpdateQueueCapacity
	}
	pool, err := NewUpdateWorkerPool(
		ctx,
		workerCount,
		queueCapacity,
		b.processUpdate,
	)
	if err != nil {
		return fmt.Errorf("не удалось запустить пул обработки Telegram updates: %w", err)
	}
	b.logger.Info("пул обработки Telegram updates запущен",
		"workers", workerCount,
		"queue_capacity_per_worker", queueCapacity,
		"queue_capacity_total", workerCount*queueCapacity,
	)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), updateWorkerShutdownTimeout)
		defer cancel()
		if err := pool.Shutdown(shutdownCtx); err != nil {
			b.logger.Warn("пул обработки Telegram updates завершил работу с ошибкой", "error", err)
		}
	}()

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
			b.logger.Warn("ошибка получения обновлений Telegram; будет повторная попытка", "error_kind", classifyTelegramAPIError(err), "error", err)
			if !wait(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = time.Second
		var retry bool
		offset, retry = b.dispatchUpdateBatch(ctx, pool, updates, offset)
		if ctx.Err() != nil {
			return nil
		}
		if retry && !wait(ctx, time.Second) {
			return nil
		}
	}
}

func isRetryableUpdateError(err error) bool {
	if err == nil {
		return false
	}
	for _, terminal := range []error{
		domain.ErrNotFound,
		domain.ErrConflict,
		domain.ErrInvalidInput,
		domain.ErrInvalidState,
		domain.ErrNoPreviousQuestion,
		domain.ErrNoNextQuestion,
		domain.ErrActiveCourse,
		domain.ErrNoCurator,
		domain.ErrLegacyProgress,
	} {
		if errors.Is(err, terminal) {
			return false
		}
	}
	return true
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
		b.logger.Warn("Telegram API временно недоступен при запуске; будет повторная попытка", "error_kind", classifyTelegramAPIError(err), "error", err)
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
		b.logger.Warn("не удалось подготовить long polling Telegram; будет повторная попытка", "error_kind", classifyTelegramAPIError(err), "error", err)
		if !wait(ctx, backoff) {
			return User{}, nil
		}
		backoff = nextBackoff(backoff)
	}
}

// handleUpdate acknowledges callback UI immediately, then delegates business
// work to processUpdate. The worker pool can call acknowledgeUpdate before queueing
// and processUpdate from a worker without sending the callback acknowledgement twice.
func (b *Bot) handleUpdate(ctx context.Context, update Update) bool {
	b.acknowledgeUpdate(ctx, update)
	return b.processUpdate(ctx, update)
}

func (b *Bot) acknowledgeUpdate(ctx context.Context, update Update) {
	if update.CallbackQuery != nil {
		b.acknowledgeCallback(ctx, *update.CallbackQuery)
	}
}

func (b *Bot) acknowledgeCallback(ctx context.Context, callback CallbackQuery) {
	if callback.ID == "" || !b.claimCallbackAcknowledgement(callback.ID) {
		return
	}
	ackCtx, cancel := context.WithTimeout(ctx, telegramCallbackTimeout)
	defer cancel()
	if err := b.api.AnswerCallbackQuery(ackCtx, callback.ID, ""); err != nil && ctx.Err() == nil {
		b.logger.Warn("не удалось подтвердить callback Telegram", "error_kind", classifyTelegramAPIError(err), "error", err)
	}
}

func (b *Bot) claimCallbackAcknowledgement(callbackID string) bool {
	b.callbackAckMu.Lock()
	defer b.callbackAckMu.Unlock()

	if b.callbackAckSeen == nil {
		b.callbackAckSeen = make(map[string]struct{})
	}
	if _, exists := b.callbackAckSeen[callbackID]; exists {
		return false
	}
	if len(b.callbackAckOrder) < callbackAckCacheCapacity {
		b.callbackAckOrder = append(b.callbackAckOrder, callbackID)
	} else {
		previous := b.callbackAckOrder[b.callbackAckNext]
		delete(b.callbackAckSeen, previous)
		b.callbackAckOrder[b.callbackAckNext] = callbackID
		b.callbackAckNext = (b.callbackAckNext + 1) % callbackAckCacheCapacity
	}
	b.callbackAckSeen[callbackID] = struct{}{}
	return true
}

func (b *Bot) processUpdate(ctx context.Context, update Update) bool {
	if update.CallbackQuery != nil {
		return b.handleCallbackWithUpdateID(ctx, update.UpdateID, *update.CallbackQuery)
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
		return b.handleTextAnswer(ctx, update.UpdateID, *message.From, *message, trimmedText)
	}

	command := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	switch command {
	case "/start":
		referralPayload := ""
		if len(fields) > 1 {
			referralPayload = strings.Join(fields[1:], " ")
		}
		return b.handleStart(ctx, message.Chat.ID, *message.From, referralPayload)
	case "/chat":
		return b.handleChatCommand(ctx, message.Chat.ID, *message.From)
	case "/link":
		return b.handleStaffLinkCommand(ctx, message.Chat.ID, *message.From, fields)
	case "/help":
		b.send(ctx, message.Chat.ID, "Этот бот предназначен для изучения Библии через уроки и общения с координатором.\n\n/start — открыть главное меню\n/chat — связаться с координатором\n/link КОД — привязать Telegram к учётной записи куратора\n/help — показать эту справку\n/cancel — отменить текущее действие.\n\nВ текстовом вопросе отправьте ответ следующим сообщением. Для общения с координатором сначала включите режим через /chat или меню.")
		return true
	case "/cancel":
		return b.handleCancelCommand(ctx, message.Chat.ID, *message.From)
	default:
		b.send(ctx, message.Chat.ID, "Пока доступны команды /start, /chat, /help и /cancel.")
		return true
	}
}

func (b *Bot) handleLinkedStaffUpdate(ctx context.Context, message Message) (bool, bool) {
	if b.staffTelegram == nil || message.From == nil {
		return false, true
	}
	// Повторная привязка проходит через новый одноразовый код из авторизованной панели.
	fields := strings.Fields(strings.TrimSpace(message.Text))
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") &&
		strings.ToLower(strings.SplitN(fields[0], "@", 2)[0]) == "/link" {
		return false, true
	}
	account, err := b.staffTelegram.FindByTelegramID(ctx, message.From.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, true
	}
	if err != nil {
		b.logger.Error("не удалось проверить Telegram-привязку сотрудника", "telegram_user_id", message.From.ID, "error", err)
		b.send(ctx, message.Chat.ID, "Не удалось проверить учётную запись. Повторите сообщение позже.")
		return true, false
	}
	if account.AccountStatus != "active" {
		b.send(ctx, message.Chat.ID, "Эта учётная запись сотрудника отключена. Уведомления не отправляются; обратитесь к администратору.")
		return true, true
	}
	if !account.IsCurator && !account.IsAdministrator {
		b.send(ctx, message.Chat.ID, "У этой учётной записи больше нет активной роли куратора или администратора. Уведомления отключены; обратитесь к администратору.")
		return true, true
	}
	command := ""
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		command = strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	}
	if command == "/start" {
		if err := b.staffTelegram.RestoreNotifications(ctx, account.StaffID); err != nil {
			b.logger.Error("не удалось восстановить уведомления сотрудника", "staff_id", account.StaffID, "error", err)
			b.send(ctx, message.Chat.ID, "Привязка активна, но не удалось восстановить уведомления. Повторите /start позже.")
			return true, false
		}
		if account.IsAdministrator {
			b.send(ctx, message.Chat.ID, "Telegram привязан к учётной записи администратора. Уведомления об эскалациях включены. Проверяйте переданные диалоги в веб-панели, в разделе «Диалоги».")
		} else {
			b.send(ctx, message.Chat.ID, "Telegram привязан к учётной записи куратора. Уведомления включены. Отвечайте ученикам в веб-панели, в разделе «Диалоги».")
		}
	} else if account.IsAdministrator {
		b.send(ctx, message.Chat.ID, "Ваша учётная запись привязана как администратор. Уведомления об эскалациях будут приходить сюда; проверяйте диалоги в веб-панели.")
	} else {
		b.send(ctx, message.Chat.ID, "Ваша учётная запись привязана как куратор. Просматривайте сообщения и отвечайте ученикам в веб-панели, в разделе «Диалоги».")
	}
	return true, true
}

func (b *Bot) handleStaffLinkCommand(ctx context.Context, chatID int64, user User, fields []string) bool {
	if b.staffTelegram == nil {
		b.send(ctx, chatID, "Привязка Telegram сотрудника пока недоступна.")
		return true
	}
	if chatID != user.ID || len(fields) != 2 {
		b.send(ctx, chatID, "Для привязки откройте личный чат с ботом и отправьте команду /link КОД из веб-панели.")
		return true
	}
	account, err := b.staffTelegram.BindTelegramAccount(ctx, user.ID, user.Username, fields[1])
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
		// The first attempt may have committed the binding and consumed the
		// one-time token before the polling offset was advanced. Recognize that
		// replay instead of telling the already-linked employee that the token
		// merely expired.
		linked, lookupErr := b.staffTelegram.FindByTelegramID(ctx, user.ID)
		if lookupErr == nil && linked.AccountStatus == "active" && (linked.IsCurator || linked.IsAdministrator) {
			b.send(ctx, chatID, "Telegram уже привязан к учётной записи «"+linked.DisplayName+"». Повторный запрос не изменил привязку.")
			return true
		}
		if lookupErr != nil && !errors.Is(lookupErr, domain.ErrNotFound) {
			b.logger.Error("не удалось проверить повторную Telegram-привязку", "telegram_user_id", user.ID, "error", lookupErr)
			return !isRetryableUpdateError(lookupErr)
		}
		b.send(ctx, chatID, "Код привязки недействителен или истёк. Создайте новый код в веб-панели.")
		return true
	}
	if errors.Is(err, domain.ErrInvalidState) {
		b.send(ctx, chatID, "Привязка недоступна: учётная запись отключена или у неё больше нет активной роли куратора либо администратора.")
		return true
	}
	if errors.Is(err, domain.ErrConflict) {
		b.send(ctx, chatID, "Этот Telegram-аккаунт уже привязан к другой учётной записи.")
		return true
	}
	if err != nil {
		b.logger.Error("не удалось привязать Telegram к учётной записи", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось завершить привязку из-за временной ошибки. Создайте новый код позже.")
		return !isRetryableUpdateError(err)
	}
	b.send(ctx, chatID, "Telegram успешно привязан к учётной записи «"+account.DisplayName+"». Уведомления о новых сообщениях будут приходить сюда. Отвечайте ученикам в веб-панели.")
	return true
}

func (b *Bot) handleChatCommand(ctx context.Context, chatID int64, user User) bool {
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Warn("не удалось определить ученика для режима общения", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте ещё раз.")
		return !isRetryableUpdateError(err)
	}
	return b.startStudentChat(ctx, chatID, 0, student)
}

func (b *Bot) startStudentChat(ctx context.Context, chatID, messageID int64, student domain.Student) bool {
	if b.conversations == nil {
		b.render(ctx, chatID, messageID, "Общение с координатором пока недоступно. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return true
	}
	if err := b.conversations.SetStudentChatMode(ctx, student.ID, true); err != nil {
		b.logger.Error("не удалось включить режим общения", "student_id", student.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось включить режим общения. Попробуйте ещё раз.", keyboard(button("Повторить", "chat:start")))
		return !isRetryableUpdateError(err)
	}
	b.render(ctx, chatID, messageID,
		"Режим общения с координатором включён.\n\nНапишите сообщение следующим сообщением. Если за вами уже закреплён координатор, сообщение будет направлено ему. Если действующего назначения ещё нет, система подберёт доступного координатора с подключённым Telegram и наименьшей нагрузкой. Выбранный режим прохождения курса не изменится.\n\nЧтобы вернуться к прохождению курсов, нажмите кнопку ниже или отправьте /cancel.",
		keyboard(button("Вернуться к прохождению курсов", "chat:cancel")))
	return true
}

func (b *Bot) handleCancelCommand(ctx context.Context, chatID int64, user User) bool {
	if b.conversations == nil {
		b.send(ctx, chatID, "Сейчас нет активного действия для отмены.")
		return true
	}
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Warn("не удалось определить ученика при отмене действия", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте снова.")
		return !isRetryableUpdateError(err)
	}
	enabled, err := b.conversations.IsStudentChatMode(ctx, student.ID)
	if err != nil {
		b.logger.Warn("не удалось проверить режим общения при отмене", "student_id", student.ID, "error", err)
		b.send(ctx, chatID, "Не удалось отменить действие из-за временной ошибки. Попробуйте ещё раз.")
		return !isRetryableUpdateError(err)
	}
	if !enabled {
		b.send(ctx, chatID, "Сейчас нет активного действия для отмены.")
		return true
	}
	if err := b.conversations.SetStudentChatMode(ctx, student.ID, false); err != nil {
		b.logger.Error("не удалось отключить режим общения", "student_id", student.ID, "error", err)
		b.send(ctx, chatID, "Не удалось выключить режим общения. Попробуйте ещё раз.")
		return !isRetryableUpdateError(err)
	}
	b.send(ctx, chatID, "Режим общения выключен. Следующие текстовые сообщения будут обрабатываться как ответы на вопросы уроков.")
	return true
}
func (b *Bot) handleStudentConversationText(ctx context.Context, student domain.Student, message Message, bodyText string) bool {
	saved, err := b.conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: student.ID, TelegramChatID: message.Chat.ID,
		TelegramMessageID: message.MessageID, BodyText: bodyText,
	})
	if errors.Is(err, domain.ErrInvalidState) {
		// Не удаляем текст и не подтверждаем как отправленный: он остаётся в истории Telegram-чата.
		b.send(ctx, message.Chat.ID, "Сейчас нет доступного координатора с подключённым Telegram. Сообщение НЕ отправлено и не сохранено. Оно остаётся в истории этого Telegram-чата: попробуйте отправить его позже.")
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
		b.send(ctx, message.Chat.ID, "Это сообщение уже принято и сохранено. Координатор увидит его в диалоге.")
	} else {
		b.send(ctx, message.Chat.ID, "Сообщение сохранено и передано координатору. Он обязательно вам ответит.")
	}
	return true
}

func (b *Bot) handleStart(ctx context.Context, chatID int64, user User, referralPayload string) bool {
	student, err := b.ensureStudent(ctx, user)
	if err != nil {
		b.logger.Error("не удалось зарегистрировать ученика Telegram", "telegram_user_id", user.ID, "error", err)
		b.send(ctx, chatID, "Не удалось завершить регистрацию из-за временной ошибки. Попробуйте ещё раз немного позже.")
		return !isRetryableUpdateError(err)
	}
	notice := ""
	if referralPayload != "" {
		referralMessage, ok := b.applyStudentReferral(ctx, student.ID, referralPayload)
		notice = referralMessage
		if !ok {
			return false
		}
	}
	return b.showMainMenu(ctx, chatID, student.ID, 0, notice)
}

func (b *Bot) applyStudentReferral(ctx context.Context, studentID int64, code string) (string, bool) {
	if b.studentReferrals == nil {
		b.logger.Error("реферальный сервис Telegram-бота не подключён", "student_id", studentID)
		return "Не удалось применить приглашение координатора. Главное меню открыто.", true
	}

	result, err := b.studentReferrals.AssignStudentByReferral(ctx, studentID, code)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrNotFound):
			return "Ссылка приглашения недействительна или координатор больше неактивен. Главное меню открыто.", true
		case errors.Is(err, domain.ErrConflict):
			return "За вами уже закреплён другой координатор. Текущее назначение не изменено.", true
		case errors.Is(err, domain.ErrInvalidState):
			return "Не удалось применить приглашение: аккаунт ученика неактивен.", true
		default:
			b.logger.Error("не удалось применить реферальное назначение куратора", "student_id", studentID, "error", err)
			return "Не удалось применить приглашение из-за временной ошибки. Повторите /start с этой ссылкой.", !isRetryableUpdateError(err)
		}
	}

	if result.Assigned {
		return "Координатор закреплён за вами. Это не меняет способ прохождения курса.", true
	}
	if result.AlreadyAssigned {
		return "Вы уже закреплены за этим координатором. Способ прохождения курса не изменён.", true
	}

	b.logger.Warn("реферальный сервис вернул пустой результат назначения", "student_id", studentID)
	return "Не удалось подтвердить назначение координатора. Главное меню открыто.", true
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
