package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const apiBaseURL = "https://api.telegram.org"

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
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type BotAPI interface {
	GetMe(context.Context) (User, error)
	GetUpdates(context.Context, int64, int) ([]Update, error)
	SendMessage(context.Context, int64, string, *InlineKeyboardMarkup) (Message, error)
	EditMessageText(context.Context, int64, int64, string, *InlineKeyboardMarkup) error
	AnswerCallbackQuery(context.Context, string, string) error
	DeleteMessage(context.Context, int64, int64) error
}

type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("Telegram API method %s failed with code %d", e.Method, e.Code)
	}
	return fmt.Sprintf("Telegram API method %s failed with code %d: %s", e.Method, e.Code, e.Description)
}

type APIClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

func NewAPIClient(token string) *APIClient {
	return newAPIClient(token, apiBaseURL, &http.Client{Timeout: 40 * time.Second})
}

func newAPIClient(token, baseURL string, client *http.Client) *APIClient {
	return &APIClient{token: strings.TrimSpace(token), baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c *APIClient) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram method %s: %w", method, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Telegram request for %s: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		// Ошибка транспорта может содержать исходный URL с токеном бота, поэтому её текст не логируем.
		return fmt.Errorf("send Telegram method %s: network request failed (%T)", method, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read Telegram response for %s: %w", method, err)
	}
	var envelope apiEnvelope
	decodeErr := json.Unmarshal(responseBody, &envelope)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		code := response.StatusCode
		description := "HTTP request failed"
		if decodeErr == nil {
			if envelope.ErrorCode > 0 {
				code = envelope.ErrorCode
			}
			if strings.TrimSpace(envelope.Description) != "" {
				description = envelope.Description
			}
		}
		switch code {
		case http.StatusUnauthorized:
			if !strings.Contains(strings.ToLower(description), "unauthorized") {
				description = "unauthorized; проверьте TELEGRAM_BOT_TOKEN: " + description
			} else {
				description += "; проверьте TELEGRAM_BOT_TOKEN"
			}
		case http.StatusConflict:
			if !strings.Contains(strings.ToLower(description), "conflict") {
				description = "conflict: " + description
			}
		}
		return &APIError{Method: method, Code: code, Description: description}
	}
	if decodeErr != nil {
		return fmt.Errorf("decode Telegram response for %s: %w", method, decodeErr)
	}
	if !envelope.OK {
		return &APIError{Method: method, Code: envelope.ErrorCode, Description: envelope.Description}
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode Telegram result for %s: %w", method, err)
		}
	}
	return nil
}

func (c *APIClient) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.call(ctx, "getMe", struct{}{}, &user)
	return user, err
}

func (c *APIClient) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error) {
	if timeoutSeconds < 0 || timeoutSeconds > 50 {
		return nil, errors.New("Telegram long-polling timeout must be between 0 and 50 seconds")
	}
	payload := struct {
		Offset         int64    `json:"offset"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{Offset: offset, Timeout: timeoutSeconds, AllowedUpdates: []string{"message", "callback_query"}}
	var updates []Update
	err := c.call(ctx, "getUpdates", payload, &updates)
	return updates, err
}

func (c *APIClient) SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (Message, error) {
	payload := struct {
		ChatID      int64                 `json:"chat_id"`
		Text        string                `json:"text"`
		ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{ChatID: chatID, Text: text, ReplyMarkup: markup}
	var message Message
	err := c.call(ctx, "sendMessage", payload, &message)
	return message, err
}

func (c *APIClient) EditMessageText(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	payload := struct {
		ChatID      int64                 `json:"chat_id"`
		MessageID   int64                 `json:"message_id"`
		Text        string                `json:"text"`
		ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{ChatID: chatID, MessageID: messageID, Text: text, ReplyMarkup: markup}
	var result json.RawMessage
	return c.call(ctx, "editMessageText", payload, &result)
}

func (c *APIClient) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	payload := struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}{CallbackQueryID: callbackID, Text: text}
	return c.call(ctx, "answerCallbackQuery", payload, nil)
}

func (c *APIClient) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
	}{ChatID: chatID, MessageID: messageID}, nil)
}
