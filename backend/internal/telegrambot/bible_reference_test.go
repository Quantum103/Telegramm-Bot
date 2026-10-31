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

func TestUnsupportedTranslationDoesNotSilentlyLinkToSynodalChapter(t *testing.T) {
	translation := "Современный перевод"
	targets := bibleChapterTargets([]domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7, TranslationName: &translation,
	}})
	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want no target for unsupported translation", targets)
	}
}
