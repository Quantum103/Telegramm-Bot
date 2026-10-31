package telegrambot

import (
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

func TestBibleChapterURLUsesBookAndChapter(t *testing.T) {
	got := bibleChapterURL(domain.BiblePassage{
		BookName: "От Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7,
	})
	const want = "https://bible.by/syn/40/4/"
	if got != want {
		t.Fatalf("chapter URL = %q, want %q", got, want)
	}
}

func TestBibleChapterURLDoesNotSilentlyChangeExplicitTranslation(t *testing.T) {
	translation := "Современный перевод"
	got := bibleChapterURL(domain.BiblePassage{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7,
		TranslationName: &translation,
	})
	if got != "" {
		t.Fatalf("chapter URL = %q, want no link for an unsupported translation", got)
	}
}

func TestBibleChapterURLSupportsEnglishBookNames(t *testing.T) {
	got := bibleChapterURL(domain.BiblePassage{
		BookName: "John", Chapter: 3, StartVerse: 16, EndVerse: 17,
	})
	const want = "https://bible.by/syn/43/3/"
	if got != want {
		t.Fatalf("chapter URL = %q, want %q", got, want)
	}
}
