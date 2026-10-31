package telegrambot

import (
	"net/url"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestBiblePassageReferenceFormatsInclusiveRange(t *testing.T) {
	got := biblePassageReference(domain.BiblePassage{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7,
	})
	if got != "Матфея 4:4-7" {
		t.Fatalf("reference = %q, want Матфея 4:4-7", got)
	}
}

func TestChapterMiniAppURLCarriesChapterAndHighlightedRange(t *testing.T) {
	targets := bibleChapterTargets([]domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7,
	}})
	if len(targets) != 1 {
		t.Fatalf("chapter targets = %d, want 1", len(targets))
	}
	got := chapterMiniAppURL("https://reader.example/miniapp", "Почему Иисус ответил?", targets[0])
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse Mini App URL: %v", err)
	}
	query := parsed.Query()
	if parsed.Scheme != "https" || parsed.Host != "reader.example" || parsed.Path != "/miniapp" {
		t.Fatalf("unexpected Mini App URL: %s", got)
	}
	for key, want := range map[string]string{
		"view": "chapter", "book_id": "40", "book": "Матфея", "chapter": "4",
		"translation": "synodal", "highlight": "4-7", "reference": "Матфея 4:4-7",
		"question": "Почему Иисус ответил?",
	} {
		if query.Get(key) != want {
			t.Errorf("query[%q] = %q, want %q", key, query.Get(key), want)
		}
	}
}

func TestBibleChapterTargetsMergeOverlappingAndAdjacentRanges(t *testing.T) {
	targets := bibleChapterTargets([]domain.BiblePassage{
		{BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7},
		{BookName: "От Матфея", Chapter: 4, StartVerse: 7, EndVerse: 9},
		{BookName: "Матфея", Chapter: 4, StartVerse: 12, EndVerse: 12},
	})
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if got, want := formatBibleVerseRanges(targets[0].ranges), "4-9,12"; got != want {
		t.Fatalf("highlight ranges = %q, want %q", got, want)
	}
}

func TestBibleChapterTargetsSeparateDifferentChapters(t *testing.T) {
	targets := bibleChapterTargets([]domain.BiblePassage{
		{BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7},
		{BookName: "Матфея", Chapter: 5, StartVerse: 1, EndVerse: 2},
	})
	if len(targets) != 2 || targets[0].chapter != 4 || targets[1].chapter != 5 {
		t.Fatalf("chapter targets = %#v, want chapters 4 and 5", targets)
	}
}

func TestChapterMiniAppButtonsUseWebAppNotExternalURLs(t *testing.T) {
	buttons := chapterWebAppButtons("https://reader.example/miniapp", "Вопрос", []domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7,
	}})
	if len(buttons) != 1 {
		t.Fatalf("buttons = %d, want 1", len(buttons))
	}
	if buttons[0].WebApp == nil || buttons[0].CallbackData != "" {
		t.Fatalf("chapter button = %#v, want WebApp button without callback data", buttons[0])
	}
	if buttons[0].Text != "📖 Прочитать всю главу" {
		t.Fatalf("button text = %q", buttons[0].Text)
	}
	parsed, err := url.Parse(buttons[0].WebApp.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "reader.example" {
		t.Fatalf("Mini App button URL = %q, want HTTPS reader URL", buttons[0].WebApp.URL)
	}
}

func TestUnsupportedTranslationDoesNotSilentlyLinkToSynodalChapter(t *testing.T) {
	translation := "Современный перевод"
	targets := bibleChapterTargets([]domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7, TranslationName: &translation,
	}})
	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want no target for unsupported translation", targets)
	}
}

func TestNormalizeWebAppBaseURLRequiresHTTPS(t *testing.T) {
	if got := normalizeWebAppBaseURL("http://reader.example/miniapp"); got != "" {
		t.Fatalf("normalized URL = %q, want empty for HTTP", got)
	}
	if got := normalizeWebAppBaseURL("https://reader.example/miniapp#fragment"); got != "https://reader.example/miniapp" {
		t.Fatalf("normalized URL = %q", got)
	}
}
