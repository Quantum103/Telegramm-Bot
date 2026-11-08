package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func makeMiniAppInitData(botToken string, userJSON string, authDate time.Time) string {
	values := url.Values{}
	values.Set("auth_date", strconv.FormatInt(authDate.Unix(), 10))
	values.Set("query_id", "AAEAAAE")
	values.Set("user", userJSON)

	keys := []string{"auth_date", "query_id", "user"}
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values.Get(key))
	}
	dataCheckString := strings.Join(pairs, "\n")

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(dataCheckString))
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	return values.Encode()
}

func TestValidateTelegramWebAppInitDataAcceptsValidSignature(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	raw := makeMiniAppInitData("bot-test-token", `{"id":123456,"first_name":"Иван","last_name":"Тестов","username":"ivan"}`, now.Add(-2*time.Minute))

	user, err := validateTelegramWebAppInitData(raw, "bot-test-token", now)
	if err != nil {
		t.Fatalf("validateTelegramWebAppInitData() error = %v", err)
	}
	if user.ID != 123456 || user.FirstName != "Иван" || user.LastName != "Тестов" || user.Username != "ivan" {
		t.Fatalf("unexpected validated Telegram user: %+v", user)
	}
}

func TestValidateTelegramWebAppInitDataRejectsTamperedUser(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	raw := makeMiniAppInitData("bot-test-token", `{"id":123456,"first_name":"Иван"}`, now.Add(-time.Minute))
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("user", `{"id":999999,"first_name":"Другой"}`)

	if _, err := validateTelegramWebAppInitData(values.Encode(), "bot-test-token", now); err == nil {
		t.Fatal("expected tampered user data to be rejected")
	}
}

func TestValidateTelegramWebAppInitDataRejectsExpiredData(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	raw := makeMiniAppInitData("bot-test-token", `{"id":123456,"first_name":"Иван"}`, now.Add(-miniAppMaxInitDataAge-time.Second))

	if _, err := validateTelegramWebAppInitData(raw, "bot-test-token", now); err == nil {
		t.Fatal("expected expired init data to be rejected")
	}
}

func TestValidateTelegramWebAppInitDataRejectsFutureData(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	raw := makeMiniAppInitData("bot-test-token", `{"id":123456,"first_name":"Иван"}`, now.Add(2*time.Minute))

	if _, err := validateTelegramWebAppInitData(raw, "bot-test-token", now); err == nil {
		t.Fatal("expected future init data to be rejected")
	}
}

func TestValidateTelegramWebAppInitDataRejectsMissingHashAndToken(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	raw := makeMiniAppInitData("bot-test-token", `{"id":123456,"first_name":"Иван"}`, now)

	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	values.Del("hash")
	if _, err := validateTelegramWebAppInitData(values.Encode(), "bot-test-token", now); err == nil {
		t.Fatal("expected missing hash to be rejected")
	}
	if _, err := validateTelegramWebAppInitData(raw, "", now); err == nil {
		t.Fatal("expected missing bot token to be rejected")
	}
}

type miniAppTestStudentRegistrar struct{}

func (miniAppTestStudentRegistrar) EnsureByTelegramID(_ context.Context, params domain.EnsureStudentParams) (domain.Student, error) {
	return domain.Student{
		ID:          42,
		TelegramID:  params.TelegramID,
		DisplayName: params.DisplayName,
	}, nil
}

type miniAppTestConversationAPI struct {
	unreadCount int64
}

func (miniAppTestConversationAPI) GetOrCreateForStudent(context.Context, int64) (domain.Conversation, error) {
	return domain.Conversation{}, nil
}

func (miniAppTestConversationAPI) ListMessagesForStudent(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error) {
	return nil, nil
}

func (miniAppTestConversationAPI) CreateStudentMiniAppMessage(context.Context, domain.CreateStudentMiniAppConversationMessageParams) (domain.ConversationMessage, error) {
	return domain.ConversationMessage{}, nil
}

func (miniAppTestConversationAPI) MarkReadForStudent(context.Context, int64, int64) (int64, error) {
	return 0, nil
}

func (api miniAppTestConversationAPI) UnreadCountForStudent(context.Context, int64) (int64, error) {
	return api.unreadCount, nil
}

func TestMiniAppConversationUnreadRequiresTelegramAuthAndReturnsCount(t *testing.T) {
	const botToken = "bot-test-token"
	now := time.Now().UTC()
	initData := makeMiniAppInitData(botToken, "{\"id\":123456,\"first_name\":\"Иван\"}", now)

	handler := NewHandlerWithTelegramMiniApp(nil, MiniAppDependencies{
		BotToken:      botToken,
		Students:      miniAppTestStudentRegistrar{},
		Conversations: miniAppTestConversationAPI{unreadCount: 7},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/miniapp/conversation/unread", nil)
	request.Header.Set(miniAppInitDataHeader, initData)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("неожиданный HTTP статус: got %d body=%s", response.Code, response.Body.String())
	}
	var body miniAppConversationUnreadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("декодировать ответ: %v", err)
	}
	if body.UnreadCount != 7 {
		t.Fatalf("неверный счётчик непрочитанных: got %d want 7", body.UnreadCount)
	}
}

func TestMiniAppConversationUnreadRejectsMissingTelegramAuth(t *testing.T) {
	handler := NewHandlerWithTelegramMiniApp(nil, MiniAppDependencies{
		BotToken:      "bot-test-token",
		Students:      miniAppTestStudentRegistrar{},
		Conversations: miniAppTestConversationAPI{unreadCount: 7},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/miniapp/conversation/unread", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("запрос без Telegram initData должен быть отклонён: got %d body=%s", response.Code, response.Body.String())
	}
}

func TestValidateTelegramWebAppInitDataRejectsDuplicateParameters(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	raw := makeMiniAppInitData("bot-test-token", `{"id":123456,"first_name":"Иван"}`, now)

	tests := []struct {
		name string
		raw  string
	}{
		{name: "duplicate user", raw: raw + "&user=" + url.QueryEscape(`{"id":999999,"first_name":"Другой"}`)},
		{name: "duplicate hash", raw: raw + "&hash=" + strings.Repeat("0", sha256.Size*2)},
		{name: "duplicate auth date", raw: raw + "&auth_date=" + strconv.FormatInt(now.Unix(), 10)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validateTelegramWebAppInitData(tt.raw, "bot-test-token", now); err == nil {
				t.Fatal("expected duplicate Telegram init data parameter to be rejected")
			}
		})
	}
}

func TestDecodeMiniAppJSONRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"known":"value","unexpected":true}`},
		{name: "trailing JSON", body: `{"known":"value"} {"known":"second"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			response := httptest.NewRecorder()
			var decoded struct {
				Known string `json:"known"`
			}
			if decodeMiniAppJSON(response, request, &decoded) {
				t.Fatal("expected malformed or unexpected JSON to be rejected")
			}
			if response.Code != http.StatusBadRequest {
				t.Fatalf("unexpected status: got %d want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDecodeMiniAppJSONRejectsOversizedBody(t *testing.T) {
	body := strings.Repeat("x", miniAppMaxBodyBytes+1)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	response := httptest.NewRecorder()
	var decoded struct {
		Known string `json:"known"`
	}

	if decodeMiniAppJSON(response, request, &decoded) {
		t.Fatal("expected oversized request body to be rejected")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d want %d", response.Code, http.StatusBadRequest)
	}
}
