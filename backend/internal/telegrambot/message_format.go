package telegrambot

import (
	"html"
	"strings"
	"unicode/utf8"
)

// telegramHTMLBuilder escapes text and counts visible runes, not HTML markup.
type telegramHTMLBuilder struct {
	builder   strings.Builder
	remaining int
	truncated bool
}

func newTelegramHTMLBuilder(maxRunes int) *telegramHTMLBuilder {
	return &telegramHTMLBuilder{remaining: maxRunes}
}

func (b *telegramHTMLBuilder) raw(markup string) {
	b.builder.WriteString(markup)
}

func (b *telegramHTMLBuilder) text(value string) {
	if value == "" || b.remaining <= 0 {
		if value != "" {
			b.truncated = true
		}
		return
	}
	runes := []rune(value)
	if len(runes) > b.remaining {
		value = string(runes[:b.remaining])
		b.truncated = true
	}
	b.builder.WriteString(html.EscapeString(value))
	b.remaining -= utf8.RuneCountInString(value)
}

func (b *telegramHTMLBuilder) String() string {
	return b.builder.String()
}
