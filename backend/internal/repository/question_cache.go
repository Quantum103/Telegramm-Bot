package repository

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	defaultQuestionCacheEntries = 32
	defaultQuestionCacheBytes   = 16 << 20
	defaultQuestionCacheTTL     = 10 * time.Minute
)

type publishedQuestionCache struct {
	mu         sync.Mutex
	entries    map[int64]*list.Element
	inFlight   map[int64]*questionCacheCall
	lru        list.List
	totalBytes int64
	maxEntries int
	maxBytes   int64
	ttl        time.Duration
	now        func() time.Time
}

type questionCacheEntry struct {
	versionID  int64
	questions  []domain.LessonQuestion
	memorySize int64
	expiresAt  time.Time
}

type questionCacheCall struct {
	done      chan struct{}
	questions []domain.LessonQuestion
	err       error
}

func newPublishedQuestionCache() *publishedQuestionCache {
	return newPublishedQuestionCacheWithConfig(
		defaultQuestionCacheEntries,
		defaultQuestionCacheBytes,
		defaultQuestionCacheTTL,
		time.Now,
	)
}

func newPublishedQuestionCacheWithConfig(
	maxEntries int,
	maxBytes int64,
	ttl time.Duration,
	now func() time.Time,
) *publishedQuestionCache {
	if maxEntries <= 0 {
		maxEntries = defaultQuestionCacheEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultQuestionCacheBytes
	}
	if ttl <= 0 {
		ttl = defaultQuestionCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &publishedQuestionCache{
		entries:    make(map[int64]*list.Element),
		inFlight:   make(map[int64]*questionCacheCall),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ttl:        ttl,
		now:        now,
	}
}

// GetOrLoad shares concurrent cold loads for one immutable lesson version.
// Cached values are copied so callers cannot mutate data visible to other requests.
func (c *publishedQuestionCache) GetOrLoad(
	ctx context.Context,
	versionID int64,
	load func() ([]domain.LessonQuestion, error),
) ([]domain.LessonQuestion, error) {
	if questions, ok := c.Get(versionID); ok {
		return questions, nil
	}

	c.mu.Lock()
	if element, ok := c.entries[versionID]; ok {
		entry := element.Value.(*questionCacheEntry)
		if !c.now().Before(entry.expiresAt) {
			c.removeElement(element)
		} else {
			c.lru.MoveToFront(element)
			questions := clonePublishedQuestions(entry.questions)
			c.mu.Unlock()
			return questions, nil
		}
	}
	if call, ok := c.inFlight[versionID]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
			return clonePublishedQuestions(call.questions), nil
		}
	}
	call := &questionCacheCall{done: make(chan struct{})}
	c.inFlight[versionID] = call
	c.mu.Unlock()

	questions, err := load()
	if err == nil {
		c.Set(versionID, questions)
	}

	c.mu.Lock()
	call.questions = clonePublishedQuestions(questions)
	call.err = err
	delete(c.inFlight, versionID)
	close(call.done)
	c.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return clonePublishedQuestions(questions), nil
}

func (c *publishedQuestionCache) Get(versionID int64) ([]domain.LessonQuestion, bool) {
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
		c.removeElement(element)
		return nil, false
	}
	c.lru.MoveToFront(element)
	return clonePublishedQuestions(entry.questions), true
}

func (c *publishedQuestionCache) Set(versionID int64, questions []domain.LessonQuestion) {
	if c == nil || versionID <= 0 || len(questions) == 0 {
		return
	}
	size := estimatePublishedQuestionsSize(questions)
	if size <= 0 || size > c.maxBytes {
		return
	}
	entry := &questionCacheEntry{
		versionID:  versionID,
		questions:  clonePublishedQuestions(questions),
		memorySize: size,
		expiresAt:  c.now().Add(c.ttl),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[versionID]; ok {
		c.removeElement(previous)
	}
	for len(c.entries) >= c.maxEntries || c.totalBytes+size > c.maxBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.removeElement(oldest)
	}
	element := c.lru.PushFront(entry)
	c.entries[versionID] = element
	c.totalBytes += size
}

func (c *publishedQuestionCache) removeElement(element *list.Element) {
	entry := element.Value.(*questionCacheEntry)
	delete(c.entries, entry.versionID)
	c.totalBytes -= entry.memorySize
	if c.totalBytes < 0 {
		c.totalBytes = 0
	}
	c.lru.Remove(element)
}

func estimatePublishedQuestionsSize(questions []domain.LessonQuestion) int64 {
	size := int64(len(questions)) * 256
	for _, question := range questions {
		size += int64(len(question.Prompt) + len(question.ExpectedAnswer) + len(question.AnswerType))
		size += optionalStringSize(question.ExpectedExplanation) + optionalStringSize(question.Note)
		size += int64(len(question.Criteria)) * 96
		for _, criterion := range question.Criteria {
			size += int64(len(criterion.Key) + len(criterion.Description))
		}
		size += int64(len(question.Options)) * 64
		for _, option := range question.Options {
			size += int64(len(option.Key) + len(option.Text))
		}
		size += int64(len(question.CommonMistakes)) * 64
		for _, mistake := range question.CommonMistakes {
			size += int64(len(mistake.Text) + len(mistake.Explanation))
		}
		size += int64(len(question.BiblePassages)) * 96
		for _, passage := range question.BiblePassages {
			size += int64(len(passage.BookName))
			size += optionalStringSize(passage.TranslationName)
			size += optionalStringSize(passage.PassageText)
			size += optionalStringSize(passage.SourceNote)
		}
	}
	return size
}

func optionalStringSize(value *string) int64 {
	if value == nil {
		return 0
	}
	return int64(len(*value))
}

func clonePublishedQuestions(questions []domain.LessonQuestion) []domain.LessonQuestion {
	if questions == nil {
		return nil
	}
	cloned := make([]domain.LessonQuestion, len(questions))
	for index, question := range questions {
		cloned[index] = question
		cloned[index].ExpectedExplanation = cloneString(question.ExpectedExplanation)
		cloned[index].Note = cloneString(question.Note)
		cloned[index].Criteria = append([]domain.QuestionCriterion(nil), question.Criteria...)
		cloned[index].Options = append([]domain.QuestionOption(nil), question.Options...)
		cloned[index].CommonMistakes = append([]domain.CommonMistake(nil), question.CommonMistakes...)
		cloned[index].BiblePassages = append([]domain.BiblePassage(nil), question.BiblePassages...)
		for passageIndex := range cloned[index].BiblePassages {
			passage := &cloned[index].BiblePassages[passageIndex]
			passage.TranslationName = cloneString(passage.TranslationName)
			passage.PassageText = cloneString(passage.PassageText)
			passage.SourceNote = cloneString(passage.SourceNote)
		}
	}
	return cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
