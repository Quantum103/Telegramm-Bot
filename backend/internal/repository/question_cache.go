package repository

import (
	"container/list"
	"sync"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	questionCacheDefaultMaxEntries = 128
	questionCacheDefaultMaxBytes   = int64(32 << 20)
	questionCacheDefaultTTL        = 15 * time.Minute
)

type questionContentCache struct {
	mu         sync.Mutex
	entries    map[int64]*list.Element
	order      *list.List
	maxEntries int
	maxBytes   int64
	ttl        time.Duration
	usedBytes  int64
	now        func() time.Time
}

type questionCacheEntry struct {
	versionID   int64
	questions   []domain.LessonQuestion
	expiresAt   time.Time
	weightBytes int64
}

func newQuestionContentCache() *questionContentCache {
	return newQuestionContentCacheWithLimits(
		questionCacheDefaultMaxEntries,
		questionCacheDefaultMaxBytes,
		questionCacheDefaultTTL,
	)
}

func newQuestionContentCacheWithLimits(maxEntries int, maxBytes int64, ttl time.Duration) *questionContentCache {
	if maxEntries <= 0 {
		maxEntries = questionCacheDefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = questionCacheDefaultMaxBytes
	}
	if ttl <= 0 {
		ttl = questionCacheDefaultTTL
	}
	return &questionContentCache{
		entries:    make(map[int64]*list.Element),
		order:      list.New(),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ttl:        ttl,
		now:        time.Now,
	}
}

// getOrLoad кэширует только опубликованный контент. Вызывающий loader должен читать
// исключительно опубликованные или архивные версии; черновики сюда не передаются.
func (c *questionContentCache) getOrLoad(
	versionID int64,
	loader func() ([]domain.LessonQuestion, error),
) ([]domain.LessonQuestion, error) {
	if questions, ok := c.get(versionID); ok {
		return questions, nil
	}
	loaded, err := loader()
	if err != nil {
		return nil, err
	}
	c.put(versionID, loaded)
	return cloneLessonQuestions(loaded), nil
}

func (c *questionContentCache) get(versionID int64) ([]domain.LessonQuestion, bool) {
	if c == nil || versionID <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[versionID]
	if !ok {
		return nil, false
	}
	entry := element.Value.(*questionCacheEntry)
	if !c.now().Before(entry.expiresAt) {
		c.remove(element)
		return nil, false
	}
	c.order.MoveToFront(element)
	return cloneLessonQuestions(entry.questions), true
}

func (c *questionContentCache) put(versionID int64, questions []domain.LessonQuestion) {
	if c == nil || versionID <= 0 {
		return
	}
	snapshot := cloneLessonQuestions(questions)
	weight := questionContentWeight(snapshot)
	if weight > c.maxBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[versionID]; ok {
		c.remove(existing)
	}
	entry := &questionCacheEntry{
		versionID:   versionID,
		questions:   snapshot,
		expiresAt:   c.now().Add(c.ttl),
		weightBytes: weight,
	}
	c.entries[versionID] = c.order.PushFront(entry)
	c.usedBytes += weight

	for c.order.Len() > c.maxEntries || c.usedBytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.remove(oldest)
	}
}

func (c *questionContentCache) remove(element *list.Element) {
	entry := element.Value.(*questionCacheEntry)
	delete(c.entries, entry.versionID)
	c.usedBytes -= entry.weightBytes
	c.order.Remove(element)
}

func questionContentWeight(questions []domain.LessonQuestion) int64 {
	weight := int64(64)
	for _, question := range questions {
		weight += int64(160 + len(question.Prompt) + len(question.ExpectedAnswer))
		weight += stringPointerWeight(question.ExpectedExplanation) + stringPointerWeight(question.Note)
		weight += int64(len(question.Criteria) * 48)
		for _, criterion := range question.Criteria {
			weight += int64(len(criterion.Key) + len(criterion.Description))
		}
		weight += int64(len(question.Options) * 40)
		for _, option := range question.Options {
			weight += int64(len(option.Key) + len(option.Text))
		}
		weight += int64(len(question.CommonMistakes) * 32)
		for _, mistake := range question.CommonMistakes {
			weight += int64(len(mistake.Text) + len(mistake.Explanation))
		}
		weight += int64(len(question.BiblePassages) * 64)
		for _, passage := range question.BiblePassages {
			weight += int64(len(passage.BookName) +
				len(questionCacheOptionalStringValue(passage.TranslationName)) +
				len(questionCacheOptionalStringValue(passage.PassageText)) +
				len(questionCacheOptionalStringValue(passage.SourceNote)))
		}
	}
	return weight
}

func stringPointerWeight(value *string) int64 {
	if value == nil {
		return 0
	}
	return int64(len(*value))
}

func questionCacheOptionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneLessonQuestions(source []domain.LessonQuestion) []domain.LessonQuestion {
	if source == nil {
		return nil
	}
	result := make([]domain.LessonQuestion, len(source))
	for index, question := range source {
		question.ExpectedExplanation = cloneStringPointer(question.ExpectedExplanation)
		question.Note = cloneStringPointer(question.Note)
		question.Criteria = append([]domain.QuestionCriterion(nil), question.Criteria...)
		question.Options = append([]domain.QuestionOption(nil), question.Options...)
		question.CommonMistakes = append([]domain.CommonMistake(nil), question.CommonMistakes...)
		question.BiblePassages = append([]domain.BiblePassage(nil), question.BiblePassages...)
		for passageIndex := range question.BiblePassages {
			passage := &question.BiblePassages[passageIndex]
			passage.TranslationName = cloneStringPointer(passage.TranslationName)
			passage.PassageText = cloneStringPointer(passage.PassageText)
			passage.SourceNote = cloneStringPointer(passage.SourceNote)
		}
		result[index] = question
	}
	return result
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
