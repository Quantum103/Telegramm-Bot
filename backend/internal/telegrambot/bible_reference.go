package telegrambot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

// Full chapter files use the traditional order of the 66 books of the Bible.
func biblePassageReference(passage domain.BiblePassage) string {
	if passage.EndVerse > passage.StartVerse {
		return fmt.Sprintf("%s %d:%d-%d", strings.TrimSpace(passage.BookName), passage.Chapter, passage.StartVerse, passage.EndVerse)
	}
	return fmt.Sprintf("%s %d:%d", strings.TrimSpace(passage.BookName), passage.Chapter, passage.StartVerse)
}

type bibleVerseRange struct {
	start int
	end   int
}

type bibleChapterTarget struct {
	bookID     int
	bookName   string
	chapter    int
	ranges     []bibleVerseRange
	references []string
}

func supportedBibleTranslation(passage domain.BiblePassage) bool {
	if passage.TranslationName == nil {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(*passage.TranslationName))
	return value == "" || strings.Contains(value, "синод") || value == "syn" || value == "synodal"
}

// bibleChapterTargets groups all references to the same chapter and merges adjacent verse ranges.
func bibleChapterTargets(passages []domain.BiblePassage) []bibleChapterTarget {
	targetsByKey := make(map[string]*bibleChapterTarget)
	for _, passage := range passages {
		if passage.Chapter <= 0 || passage.StartVerse <= 0 || !supportedBibleTranslation(passage) {
			continue
		}
		bookName := strings.TrimSpace(passage.BookName)
		bookID, ok := bibletext.BookID(bookName)
		if !ok {
			continue
		}
		end := passage.EndVerse
		if end < passage.StartVerse {
			end = passage.StartVerse
		}
		key := fmt.Sprintf("%d:%d", bookID, passage.Chapter)
		target := targetsByKey[key]
		if target == nil {
			target = &bibleChapterTarget{bookID: bookID, bookName: bookName, chapter: passage.Chapter}
			targetsByKey[key] = target
		}
		target.ranges = append(target.ranges, bibleVerseRange{start: passage.StartVerse, end: end})
		target.references = append(target.references, biblePassageReference(passage))
	}

	targets := make([]bibleChapterTarget, 0, len(targetsByKey))
	for _, target := range targetsByKey {
		target.ranges = mergeBibleVerseRanges(target.ranges)
		target.references = uniqueStrings(target.references)
		targets = append(targets, *target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].bookID != targets[j].bookID {
			return targets[i].bookID < targets[j].bookID
		}
		return targets[i].chapter < targets[j].chapter
	})
	return targets
}

func mergeBibleVerseRanges(input []bibleVerseRange) []bibleVerseRange {
	if len(input) == 0 {
		return nil
	}
	ranges := append([]bibleVerseRange(nil), input...)
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		return ranges[i].end < ranges[j].end
	})
	merged := make([]bibleVerseRange, 0, len(ranges))
	for _, current := range ranges {
		lastIndex := len(merged) - 1
		if lastIndex >= 0 && current.start <= merged[lastIndex].end+1 {
			if current.end > merged[lastIndex].end {
				merged[lastIndex].end = current.end
			}
			continue
		}
		merged = append(merged, current)
	}
	return merged
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func formatBibleVerseRanges(ranges []bibleVerseRange) string {
	parts := make([]string, 0, len(ranges))
	for _, value := range ranges {
		if value.start == value.end {
			parts = append(parts, strconv.Itoa(value.start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", value.start, value.end))
		}
	}
	return strings.Join(parts, ",")
}
