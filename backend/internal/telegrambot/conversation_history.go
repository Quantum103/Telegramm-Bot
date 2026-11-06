package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

// StudentConversationHistoryAPI is an optional extension: older test doubles
// and alternate chat providers do not need to implement history access.
type StudentConversationHistoryAPI interface {
	GetOrCreateForStudent(context.Context, int64) (domain.Conversation, error)
	ListMessagesForStudent(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error)
	MarkReadForStudent(context.Context, int64, int64) (int64, error)
}

const (
	studentConversationHistoryFetchLimit       = 51
	studentConversationHistoryTextLimit        = 3600
	studentConversationHistoryLongMessageLimit = 2800
)

func (b *Bot) showStudentConversationHistory(
	ctx context.Context,
	chatID int64,
	student domain.Student,
	messageID int64,
	beforeMessageID int64,
) bool {
	historyAPI, ok := b.conversations.(StudentConversationHistoryAPI)
	if !ok {
		b.render(ctx, chatID, messageID,
			"Просмотр истории переписки пока недоступен. Вы можете продолжить общение с координатором.",
			keyboard(button("Связаться с координатором", "chat:start"), button("К курсам", "courses")))
		return true
	}

	conversation, err := historyAPI.GetOrCreateForStudent(ctx, student.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidState) {
			b.render(ctx, chatID, messageID, "История переписки сейчас недоступна. Вернитесь к курсам или попробуйте позже.",
				keyboard(button("Повторить", "chat:history"), button("К курсам", "courses")))
			return true
		}
		b.logger.Error("не удалось получить диалог ученика для истории", "student_id", student.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить историю переписки. Попробуйте ещё раз.",
			keyboard(button("Повторить", "chat:history"), button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}

	messages, err := historyAPI.ListMessagesForStudent(
		ctx, student.ID, conversation.ID, beforeMessageID, studentConversationHistoryFetchLimit,
	)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			b.render(ctx, chatID, messageID, "Эта переписка больше недоступна. Откройте историю из меню курсов.",
				keyboard(button("К курсам", "courses")))
			return true
		}
		b.logger.Error("не удалось загрузить историю сообщений ученика",
			"student_id", student.ID, "conversation_id", conversation.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить историю переписки. Попробуйте ещё раз.",
			keyboard(button("Повторить", "chat:history"), button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}

	if _, err := historyAPI.MarkReadForStudent(ctx, student.ID, conversation.ID); err != nil {
		b.logger.Warn("не удалось отметить историю ученика прочитанной",
			"student_id", student.ID, "conversation_id", conversation.ID, "error", err)
	}

	if len(messages) == 0 {
		b.render(ctx, chatID, messageID, "История переписки с координатором пока пуста. Напишите координатору, чтобы начать диалог.",
			markup([][]InlineKeyboardButton{
				{button("Написать координатору", "chat:start")},
				{button("К курсам", "courses")},
			}))
		return true
	}

	text, olderCursor, hasOlder := formatStudentConversationHistory(messages)
	rows := make([][]InlineKeyboardButton, 0, 4)
	if hasOlder && olderCursor > 0 {
		rows = append(rows, []InlineKeyboardButton{button(
			"Более ранние сообщения", fmt.Sprintf("chat:history:older:%d", olderCursor),
		)})
	}
	if beforeMessageID > 0 {
		rows = append(rows, []InlineKeyboardButton{button("Последние сообщения", "chat:history")})
	}
	rows = append(rows, []InlineKeyboardButton{button("Написать координатору", "chat:start")})
	rows = append(rows, []InlineKeyboardButton{button("К курсам", "courses")})
	b.render(ctx, chatID, messageID, text, markup(rows))
	return true
}

// listMessagesForStudent returns newest-first. The Telegram screen displays
// each page chronologically and uses the oldest visible ID as the next cursor.
func formatStudentConversationHistory(messages []domain.ConversationMessage) (string, int64, bool) {
	const header = "История переписки с координатором\n\n"
	blocks := make([]string, 0, len(messages))
	shown := 0
	usedRunes := utf8.RuneCountInString(header)

	for _, message := range messages {
		body := "[текст сообщения отсутствует]"
		if message.BodyText != nil && strings.TrimSpace(*message.BodyText) != "" {
			body = strings.TrimSpace(*message.BodyText)
		}
		sender := "Координатор"
		if message.SenderType == "student" {
			sender = "Вы"
		}
		timestamp := message.CreatedAt.UTC().Format("02.01.2006 15:04 UTC")
		block := fmt.Sprintf("%s · %s\n%s", sender, timestamp, body)
		separatorRunes := 0
		if shown > 0 {
			separatorRunes = 2
		}
		blockRunes := utf8.RuneCountInString(block) + separatorRunes
		if usedRunes+blockRunes > studentConversationHistoryTextLimit {
			if shown > 0 {
				break
			}
			body = truncateHistoryText(body, studentConversationHistoryLongMessageLimit)
			body += "\n[Сообщение сокращено в этом просмотре. Полный текст остаётся в истории Telegram.]"
			block = fmt.Sprintf("%s · %s\n%s", sender, timestamp, body)
			blockRunes = utf8.RuneCountInString(block)
			if usedRunes+blockRunes > studentConversationHistoryTextLimit {
				break
			}
		}
		blocks = append(blocks, block)
		usedRunes += blockRunes
		shown++
	}

	if shown == 0 {
		return header + "Не удалось отобразить сообщения на этой странице.", 0, false
	}

	for left, right := 0, len(blocks)-1; left < right; left, right = left+1, right-1 {
		blocks[left], blocks[right] = blocks[right], blocks[left]
	}
	text := header + strings.Join(blocks, "\n\n")
	cursor := messages[shown-1].ID
	hasOlder := shown < len(messages) || len(messages) >= studentConversationHistoryFetchLimit
	return text, cursor, hasOlder
}

func truncateHistoryText(value string, maxRunes int) string {
	runes := []rune(value)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
