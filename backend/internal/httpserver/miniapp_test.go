package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
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
