package telegrambot

import (
	"context"
	"fmt"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const studentWelcomeText = `Здравствуйте! Вы попали в чат, где можно изучать Библию в спокойном для себя темпе. Есть выбор прохождения: с координатором или самостоятельно.

В чём особенности?

⭐ С координатором — за вами закрепляется человек, который будет вам помогать. Вы сможете общаться, и самое главное — всё полностью анонимно.

Самостоятельно — все уроки будут проходить автоматически, но вы всегда сможете нажать «Связаться с координатором» и написать ему, если появится сложный вопрос. Вам обязательно ответят!`

type studentCabinetReader interface {
	GetStudentCabinet(context.Context, int64) (domain.StudentCabinet, error)
}

func (b *Bot) showMainMenu(ctx context.Context, chatID, studentID, messageID int64, notice string) bool {
	if err := b.leaveStudentInteraction(ctx, studentID); err != nil {
		b.logger.Warn("не удалось завершить предыдущее взаимодействие перед открытием главного меню", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID,
			"Не удалось открыть главное меню. Попробуйте ещё раз.",
			keyboard(button("Повторить", "home:main")))
		return !isRetryableUpdateError(err)
	}

	text := studentWelcomeText
	if strings.TrimSpace(notice) != "" {
		text += "\n\n" + strings.TrimSpace(notice)
	}
	rows := make([][]InlineKeyboardButton, 0, 3)
	if b.webAppURL != "" {
		rows = append(rows, []InlineKeyboardButton{{Text: "Открыть приложение", WebApp: &WebAppInfo{URL: b.webAppURL}}})
	}
	rows = append(rows,
		[]InlineKeyboardButton{button("Личный кабинет", "home:account")},
		[]InlineKeyboardButton{button("Начать прохождение уроков", "home:lessons")},
	)
	b.render(ctx, chatID, messageID, text, markup(rows))
	return true
}

func (b *Bot) showStudentCabinet(ctx context.Context, chatID, studentID, messageID int64) bool {
	if err := b.leaveStudentInteraction(ctx, studentID); err != nil {
		b.logger.Warn("не удалось завершить взаимодействие перед открытием личного кабинета", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID,
			"Не удалось открыть личный кабинет. Попробуйте ещё раз.",
			keyboard(button("Повторить", "home:account"), button("Главное меню", "home:main")))
		return !isRetryableUpdateError(err)
	}

	reader, ok := b.learning.(studentCabinetReader)
	if !ok {
		b.render(ctx, chatID, messageID,
			"Личный кабинет временно недоступен. Попробуйте позже.",
			keyboard(button("Главное меню", "home:main")))
		return true
	}

	cabinet, err := reader.GetStudentCabinet(ctx, studentID)
	if err != nil {
		b.logger.Error("не удалось загрузить личный кабинет ученика", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID,
			"Не удалось загрузить личный кабинет. Попробуйте ещё раз.",
			keyboard(button("Повторить", "home:account"), button("Главное меню", "home:main")))
		return !isRetryableUpdateError(err)
	}

	var text strings.Builder
	text.WriteString("👤 Личный кабинет\n\n")
	fmt.Fprintf(&text, "Дата регистрации: %s\n", cabinet.RegisteredAt.Format("02.01.2006"))
	fmt.Fprintf(&text, "Завершено или передано координатору курсов: %d\n", cabinet.CompletedCourses)
	fmt.Fprintf(&text, "Пройдено или отправлено координатору уроков: %d\n", cabinet.CompletedLessons)
	if cabinet.UnreadConversationMessages > 0 {
		fmt.Fprintf(&text, "Непрочитанные сообщения координатора: %d\n", cabinet.UnreadConversationMessages)
	}
	text.WriteString("\nВаши курсы:")
	if len(cabinet.Courses) == 0 {
		text.WriteString("\n\nВы пока не начали ни одного курса.")
	} else {
		for _, course := range cabinet.Courses {
			progressVerb := "пройдено"
			if course.Mode == "curator" {
				progressVerb = "передано координатору"
			}
			fmt.Fprintf(
				&text,
				"\n\n• %s\n  %s · %d из %d уроков %s",
				truncate(course.Title, 140),
				studentCourseStatusLabel(course.Status, course.Mode),
				course.CompletedLessons,
				course.TotalLessons,
				progressVerb,
			)
			if course.Mode == "curator" {
				if course.PendingReviews > 0 {
					fmt.Fprintf(&text, "\n  Ожидают проверки ответов: %d", course.PendingReviews)
				}
				if course.NeedsRevisionAnswers > 0 {
					fmt.Fprintf(&text, "\n  Замечания координатора: %d", course.NeedsRevisionAnswers)
				}
			}
		}
	}

	historyLabel := "История переписки"
	if cabinet.UnreadConversationMessages > 0 {
		historyLabel = fmt.Sprintf("История переписки · %d", cabinet.UnreadConversationMessages)
	}
	b.render(ctx, chatID, messageID, text.String(), markup([][]InlineKeyboardButton{
		{button("Начать прохождение уроков", "home:lessons")},
		{button(historyLabel, "chat:history")},
		{button("Написать координатору", "chat:start")},
		{button("Главное меню", "home:main")},
	}))
	return true
}

func studentCourseStatusLabel(status, mode string) string {
	if mode == "curator" {
		switch status {
		case "completed":
			return "Все уроки отправлены"
		case "in_progress":
			return "С координатором · в процессе"
		case "awaiting_curator":
			return "Ожидает координатора"
		case "paused":
			return "Приостановлен"
		default:
			return "Статус неизвестен"
		}
	}

	switch status {
	case "completed":
		return "Завершён самостоятельно"
	case "in_progress":
		return "Самостоятельно · в процессе"
	case "awaiting_curator":
		return "Ожидает координатора"
	case "paused":
		return "Приостановлен"
	default:
		return "Статус неизвестен"
	}
}
