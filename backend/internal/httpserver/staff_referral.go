package httpserver

import (
	"net/http"
	"net/url"
	"strings"
)

type staffReferralLinkResponse struct {
	Code string `json:"code"`
	URL  string `json:"url,omitempty"`
}

func (h handlers) staffReferralLink(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	referral, err := h.staffReferral.GetOrCreateReferralCode(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить реферальный код куратора")
		return
	}

	response := staffReferralLinkResponse{Code: referral.Code}
	if h.botUsernameProvider != nil {
		username, usernameErr := h.botUsernameProvider.GetBotUsername(r.Context())
		if usernameErr != nil {
			h.logger.Warn("не удалось получить username Telegram-бота для реферальной ссылки", "staff_id", identity.StaffID, "error", usernameErr)
		} else {
			username = strings.TrimPrefix(strings.TrimSpace(username), "@")
			if validTelegramBotUsername(username) {
				response.URL = "https://t.me/" + username + "?start=" + url.QueryEscape(referral.Code)
			} else {
				h.logger.Warn("Telegram Bot API вернул некорректный username", "staff_id", identity.StaffID)
			}
		}
	}

	writeJSON(w, http.StatusOK, response)
}

func validTelegramBotUsername(username string) bool {
	if len(username) < 5 || len(username) > 32 {
		return false
	}
	for _, char := range username {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}
