package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/coder/websocket"
)

type fakeRealtimeConversationAPI struct {
	events chan struct{}
}

func (f *fakeRealtimeConversationAPI) ListForCurator(context.Context, int64) ([]domain.ConversationSummary, error) {
	return nil, nil
}

func (f *fakeRealtimeConversationAPI) ListMessagesForCurator(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error) {
	return nil, nil
}

func (f *fakeRealtimeConversationAPI) SendCuratorMessage(context.Context, int64, int64, string) (domain.ConversationMessage, error) {
	return domain.ConversationMessage{}, nil
}

func (f *fakeRealtimeConversationAPI) MarkReadForCurator(context.Context, int64, int64) (int64, error) {
	return 0, nil
}

func (f *fakeRealtimeConversationAPI) UnreadCountForCurator(context.Context, int64) (int64, error) {
	return 0, nil
}

func (f *fakeRealtimeConversationAPI) SubscribeConversationEvents() (<-chan struct{}, func()) {
	return f.events, func() {}
}

func TestStaffConversationWebSocketStreamsRefreshSignals(t *testing.T) {
	conversations := &fakeRealtimeConversationAPI{events: make(chan struct{}, 1)}
	authAPI := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 77, DisplayName: "Куратор", Email: "curator@example.test",
		Roles: []string{"curator"}, ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := newHandlerWithManagementAndConversations(
		fakePinger{}, testLogger(), nil, authAPI, nil, nil, conversations, false, nil,
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	headers := make(http.Header)
	headers.Set("Origin", server.URL)
	headers.Set("Cookie", staffSessionCookieName+"=valid-session")
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/staff/conversations/events"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx, websocketURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if response != nil {
			t.Fatalf("open WebSocket: %v (HTTP %d)", err, response.StatusCode)
		}
		t.Fatalf("open WebSocket: %v", err)
	}
	defer connection.CloseNow()

	conversations.events <- struct{}{}
	messageType, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatalf("read WebSocket refresh event: %v", err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("WebSocket message type = %v, want text", messageType)
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode refresh event: %v", err)
	}
	if event.Type != "conversation_changed" {
		t.Fatalf("event type = %q, want conversation_changed", event.Type)
	}
	if strings.Contains(string(payload), "Помогите") || len(payload) > 100 {
		t.Fatalf("WebSocket must contain only a refresh signal, got %q", payload)
	}
}

func TestStaffConversationWebSocketRejectsCrossOrigin(t *testing.T) {
	conversations := &fakeRealtimeConversationAPI{events: make(chan struct{}, 1)}
	authAPI := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 77, Email: "curator@example.test", Roles: []string{"curator"},
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := newHandlerWithManagementAndConversations(
		fakePinger{}, testLogger(), nil, authAPI, nil, nil, conversations, false, nil,
	)
	request := httptest.NewRequest(http.MethodGet, "http://panel.example.test/api/v1/staff/conversations/events", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}
