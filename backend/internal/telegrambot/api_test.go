package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIClientGetMe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/getMe" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 7, "first_name": "StudyBot", "username": "study_test_bot"},
		})
	}))
	defer server.Close()

	client := newAPIClient("test-token", server.URL, &http.Client{Timeout: time.Second})
	user, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe returned error: %v", err)
	}
	if user.ID != 7 || user.Username != "study_test_bot" {
		t.Fatalf("unexpected bot identity: %+v", user)
	}
}

func TestAPIClientErrorDoesNotExposeToken(t *testing.T) {
	const secret = "secret-token-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	client := newAPIClient(secret, server.URL, &http.Client{Timeout: time.Second})
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("GetMe should fail for unauthorized token")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked in error: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestAPIClientPreservesTelegramEditErrorDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`))
	}))
	defer server.Close()

	client := newAPIClient("test-token", server.URL, &http.Client{Timeout: time.Second})
	err := client.EditMessageText(context.Background(), 10, 20, "same text", nil)
	if err == nil {
		t.Fatal("EditMessageText should expose the Bot API error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !isMessageNotModified(err) {
		t.Fatalf("message-not-modified error was not preserved: %v", err)
	}
}

func TestAPIClientTransportErrorDoesNotExposeToken(t *testing.T) {
	const secret = "very-secret-bot-token"
	client := newAPIClient(secret, "https://telegram.test", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	})
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("GetMe should fail when the HTTP transport fails")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked token: %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestBotRunStopsWhenContextIsCancelled(t *testing.T) {
	api := &testAPI{updatesWaitForCancel: true}
	bot := NewBot(api, testUpdateHandler{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

type testUpdateHandler struct{}

func (testUpdateHandler) HandleUpdate(context.Context, Update) error { return nil }

type testAPI struct {
	updatesWaitForCancel bool
}

func (a *testAPI) GetMe(context.Context) (User, error) {
	return User{ID: 1, Username: "test_bot"}, nil
}
func (a *testAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	if a.updatesWaitForCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, nil
}
func (*testAPI) SendMessage(context.Context, int64, string, *InlineKeyboardMarkup) (Message, error) {
	return Message{MessageID: 1}, nil
}
func (*testAPI) EditMessageText(context.Context, int64, int64, string, *InlineKeyboardMarkup) error {
	return nil
}
func (*testAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*testAPI) DeleteMessage(context.Context, int64, int64) error         { return nil }
