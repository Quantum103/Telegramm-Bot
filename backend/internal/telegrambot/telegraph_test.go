package telegrambot

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func TestNewTelegraphPublisherRequiresAccessToken(t *testing.T) {
	if publisher := NewTelegraphPublisher("  "); publisher != nil {
		t.Fatal("NewTelegraphPublisher() should return nil without an access token")
	}
}

func TestBuildTelegraphChapterContentFormatsEveryVerse(t *testing.T) {
	nodes, err := buildTelegraphChapterContent(bibletext.Chapter{
		Book: "Матфея", Chapter: 4, Translation: "Синодальный перевод",
		Verses: []bibletext.Verse{
			{Number: 1, Text: "Первый стих."},
			{Number: 2, Text: "Второй стих."},
		},
	})
	if err != nil {
		t.Fatalf("buildTelegraphChapterContent() error = %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("node count = %d, want one plain-text paragraph per verse", len(nodes))
	}
	if nodes[0].Tag != "p" || nodes[0].Children[0] != "1. Первый стих." {
		t.Fatalf("first verse = %+v", nodes[0])
	}
	if nodes[1].Tag != "p" || nodes[1].Children[0] != "2. Второй стих." {
		t.Fatalf("second verse = %+v", nodes[1])
	}
}

func TestTelegraphCreatePagePostsContentAndReturnsArticleURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("access_token"); got != "test-secret" {
			t.Errorf("access_token = %q", got)
		}
		if got := r.Form.Get("title"); got != "Матфея — глава 4" {
			t.Errorf("title = %q", got)
		}
		var nodes []telegraphNode
		if err := json.Unmarshal([]byte(r.Form.Get("content")), &nodes); err != nil {
			t.Errorf("content JSON error = %v", err)
		}
		if len(nodes) != 1 || nodes[0].Tag != "p" {
			t.Errorf("content nodes = %+v", nodes)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"url":"https://telegra.ph/Matfeya-4-01-01"}}`))
	}))
	defer server.Close()

	publisher := NewTelegraphPublisher("test-secret")
	publisher.apiURL = server.URL
	publisher.httpClient = server.Client()

	pageURL, err := publisher.createPage(context.Background(), "Матфея — глава 4", []telegraphNode{
		{Tag: "p", Children: []any{"Полный текст."}},
	})
	if err != nil {
		t.Fatalf("createPage() error = %v", err)
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "telegra.ph" {
		t.Fatalf("page URL = %q, want Telegraph HTTPS URL", pageURL)
	}
}

func TestIsTelegraphPageURLRejectsNonTelegraphHosts(t *testing.T) {
	for _, value := range []string{
		"https://example.com/article",
		"http://telegra.ph/article",
		"https://telegra.ph",
		"https://user@telegra.ph/article",
	} {
		if isTelegraphPageURL(value) {
			t.Errorf("isTelegraphPageURL(%q) = true, want false", value)
		}
	}
	if !isTelegraphPageURL("https://telegra.ph/Article-01-01") {
		t.Fatal("valid Telegraph article URL was rejected")
	}
}

type fakeBibleChapterReader struct {
	mu      sync.Mutex
	chapter bibletext.Chapter
	err     error
	calls   int
}

func (reader *fakeBibleChapterReader) GetChapter(_ context.Context, bookID, chapterNumber int) (bibletext.Chapter, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.calls++
	if reader.err != nil {
		return bibletext.Chapter{}, reader.err
	}
	if bookID != 40 || chapterNumber != reader.chapter.Chapter {
		return bibletext.Chapter{}, bibletext.ErrInvalidChapter
	}
	chapter := reader.chapter
	chapter.Verses = append([]bibletext.Verse(nil), reader.chapter.Verses...)
	return chapter, nil
}

func (reader *fakeBibleChapterReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type fakeTelegraphLinkStore struct {
	mu            sync.Mutex
	links         map[string]domain.TelegraphChapterLink
	chapter       domain.BibleChapter
	chapterExists bool
	chapterReads  int
	linkReads     int
	linkWrites    int
}

func newFakeTelegraphLinkStore(chapterExists bool) *fakeTelegraphLinkStore {
	chapter := testTelegraphChapter()
	verses := make([]domain.BibleVerse, len(chapter.Verses))
	for index, verse := range chapter.Verses {
		verses[index] = domain.BibleVerse{Number: verse.Number, Text: verse.Text}
	}
	return &fakeTelegraphLinkStore{
		links: make(map[string]domain.TelegraphChapterLink),
		chapter: domain.BibleChapter{
			TranslationCode: "synodal", BookID: 40, BookName: chapter.Book,
			ChapterNumber: chapter.Chapter, Verses: verses,
			SourceProvider: "midvash", SourceVersion: "synodal",
			ContentSHA256: strings.Repeat("a", 64), FetchedAt: time.Now().UTC(),
		},
		chapterExists: chapterExists,
	}
}

func telegraphTestLinkKey(translation string, bookID, chapter int, digest string) string {
	return fmt.Sprintf("%s:%d:%d:%s", translation, bookID, chapter, digest)
}

func (store *fakeTelegraphLinkStore) GetChapter(
	_ context.Context,
	translation string,
	bookID, chapter int,
) (domain.BibleChapter, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.chapterReads++
	if !store.chapterExists || translation != store.chapter.TranslationCode ||
		bookID != store.chapter.BookID || chapter != store.chapter.ChapterNumber {
		return domain.BibleChapter{}, domain.ErrNotFound
	}
	copy := store.chapter
	copy.Verses = append([]domain.BibleVerse(nil), store.chapter.Verses...)
	return copy, nil
}

func (store *fakeTelegraphLinkStore) GetTelegraphLink(
	_ context.Context,
	translation string,
	bookID, chapter int,
	digest string,
) (domain.TelegraphChapterLink, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.linkReads++
	link, ok := store.links[telegraphTestLinkKey(translation, bookID, chapter, digest)]
	if !ok {
		return domain.TelegraphChapterLink{}, domain.ErrNotFound
	}
	return link, nil
}

func (store *fakeTelegraphLinkStore) SaveTelegraphLink(
	_ context.Context,
	link domain.TelegraphChapterLink,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.linkWrites++
	if !store.chapterExists {
		return domain.ErrNotFound
	}
	key := telegraphTestLinkKey(link.TranslationCode, link.BookID, link.ChapterNumber, link.ContentSHA256)
	if _, exists := store.links[key]; !exists {
		store.links[key] = link
	}
	return nil
}

func (store *fakeTelegraphLinkStore) seedCurrentChapterLink(chapter bibletext.Chapter, pageURL string) error {
	nodes, err := buildTelegraphChapterContent(chapter)
	if err != nil {
		return err
	}
	digest, err := telegraphContentSHA256(nodes)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.chapterExists = true
	store.links[telegraphTestLinkKey("synodal", 40, chapter.Chapter, digest)] = domain.TelegraphChapterLink{
		TranslationCode: "synodal",
		BookID:          40,
		ChapterNumber:   chapter.Chapter,
		ContentSHA256:   digest,
		PageURL:         pageURL,
	}
	return nil
}

func (store *fakeTelegraphLinkStore) linkCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.links)
}

func testTelegraphChapter() bibletext.Chapter {
	return bibletext.Chapter{
		Book:        "Матфея",
		Chapter:     4,
		Translation: "Синодальный перевод",
		Verses: []bibletext.Verse{
			{Number: 1, Text: "Тогда Иисус возведен был Духом в пустыню."},
			{Number: 2, Text: "И, постившись сорок дней и сорок ночей, напоследок взалкал."},
		},
	}
}

func newTestTelegraphPublisher(
	token string,
	reader BibleChapterReader,
	store TelegraphChapterLinkStore,
	server *httptest.Server,
) *TelegraphPublisher {
	publisher := NewTelegraphPublisherWithDependencies(token, reader, store)
	if publisher != nil && server != nil {
		publisher.apiURL = server.URL
		publisher.httpClient = server.Client()
	}
	return publisher
}

func TestTelegraphPublisherUsesBibleChapterServiceAndPersistentPostgresLink(t *testing.T) {
	var pageCreations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCreations.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm(): %v", err)
		}
		if r.Form.Get("access_token") != "test-secret" {
			t.Errorf("Telegraph access token was not forwarded")
		}
		if r.Form.Get("title") != "Матфея — глава 4" {
			t.Errorf("page title = %q", r.Form.Get("title"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"url":"https://telegra.ph/Matfeya-4-01-01"}}`))
	}))
	defer server.Close()

	reader := &fakeBibleChapterReader{chapter: testTelegraphChapter()}
	store := newFakeTelegraphLinkStore(true)
	firstPublisher := newTestTelegraphPublisher("test-secret", reader, store, server)

	firstURL, err := firstPublisher.PublishChapter(context.Background(), ChapterReference{
		BookID: 40, BookName: "Матфея", Chapter: 4, Translation: "synodal",
	})
	if err != nil {
		t.Fatalf("first PublishChapter(): %v", err)
	}

	// Новый объект имитирует перезапуск backend: ссылку должен вернуть PostgreSQL, а не RAM-кэш.
	restartedPublisher := newTestTelegraphPublisher("test-secret", reader, store, server)
	secondURL, err := restartedPublisher.PublishChapter(context.Background(), ChapterReference{
		BookID: 40, BookName: "Матфея", Chapter: 4, Translation: "synodal",
	})
	if err != nil {
		t.Fatalf("PublishChapter() after restart: %v", err)
	}
	if firstURL != secondURL || firstURL != "https://telegra.ph/Matfeya-4-01-01" {
		t.Fatalf("URLs differ: first=%q second=%q", firstURL, secondURL)
	}
	if pageCreations.Load() != 1 {
		t.Fatalf("created Telegraph pages = %d, want 1", pageCreations.Load())
	}
	if reader.callCount() != 2 || store.linkCount() != 1 {
		t.Fatalf("reader calls=%d, saved links=%d; want 2 and 1", reader.callCount(), store.linkCount())
	}
	for key := range store.links {
		digest := key[strings.LastIndex(key, ":")+1:]
		if len(digest) != 64 {
			t.Fatalf("stored SHA256 has length %d, want 64", len(digest))
		}
		if _, err := hex.DecodeString(digest); err != nil {
			t.Fatalf("stored SHA256 is not hexadecimal: %q", digest)
		}
	}
}

func TestTelegraphPublisherReturnsSafeUnavailableErrorWhenChapterIsNotStored(t *testing.T) {
	reader := &fakeBibleChapterReader{chapter: testTelegraphChapter()}
	store := newFakeTelegraphLinkStore(false)
	publisher := NewTelegraphPublisherWithDependencies("test-secret", reader, store)

	_, err := publisher.PublishChapter(context.Background(), ChapterReference{
		BookID: 40, BookName: "Матфея", Chapter: 4, Translation: "synodal",
	})
	if !errors.Is(err, service.ErrBibleChapterUnavailable) {
		t.Fatalf("error = %v, want ErrBibleChapterUnavailable", err)
	}
	if store.linkCount() != 0 {
		t.Fatalf("link was stored without a persisted chapter: %d", store.linkCount())
	}
}

func TestTelegraphPublisherDoesNotQueryLinkStoreWhenChapterServiceIsUnavailable(t *testing.T) {
	reader := &fakeBibleChapterReader{err: service.ErrBibleChapterUnavailable}
	store := newFakeTelegraphLinkStore(true)
	publisher := NewTelegraphPublisherWithDependencies("test-secret", reader, store)

	_, err := publisher.PublishChapter(context.Background(), ChapterReference{
		BookID: 40, BookName: "Матфея", Chapter: 4, Translation: "synodal",
	})
	if !errors.Is(err, service.ErrBibleChapterUnavailable) {
		t.Fatalf("error = %v, want ErrBibleChapterUnavailable", err)
	}
	if store.chapterReads != 0 || store.linkReads != 0 {
		t.Fatalf("link store reads = chapter:%d link:%d, want 0", store.chapterReads, store.linkReads)
	}
}

func TestTelegramQuestionUsesCachedTelegraphLinkWithoutExternalRequests(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	passageText := "Он же сказал ему в ответ: написано: не хлебом одним будет жить человек."
	detail := catalog.details[100]
	detail.Questions[0].Prompt = "Почему Иисус ответил словами Писания?"
	detail.Questions[0].BiblePassages = []domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7, PassageText: &passageText,
	}}
	catalog.details[100] = detail

	reader := &fakeBibleChapterReader{err: errors.New("внешний источник не должен вызываться при рендеринге")}
	store := newFakeTelegraphLinkStore(true)
	if err := store.seedCurrentChapterLink(testTelegraphChapter(), "https://telegra.ph/Matfeya-4-01-01"); err != nil {
		t.Fatalf("подготовить готовую ссылку: %v", err)
	}
	publisher := NewTelegraphPublisherWithDependencies("test-secret", reader, store)
	bot := NewBotWithChapterPublisher(api, students, catalog, learning, nil, publisher)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	last := lastEdit(t, api)
	for _, expected := range []string{
		"Курс о Евангелиях",
		"Задание: 1 / 2",
		"❕<b>ВОПРОС</b>",
		"- Почему Иисус ответил словами Писания?",
		"• Прочтите Библейский текст:",
		"<pre>Он же сказал ему в ответ",
		"Матфея 4:4-7",
	} {
		if !strings.Contains(last.text, expected) {
			t.Fatalf("question screen is missing %q: %s", expected, last.text)
		}
	}

	if !strings.Contains(last.text, `<a href="https://telegra.ph/Matfeya-4-01-01"><u>📖 Прочитать всю главу</u></a>`) {
		t.Fatalf("question text is missing the underlined Telegraph link: %s", last.text)
	}
	for _, row := range last.markup.InlineKeyboard {
		for _, item := range row {
			if item.URL != "" {
				t.Fatalf("chapter URL must be an inline text link, not a keyboard button: %+v", item)
			}
		}
	}
	if reader.callCount() != 0 {
		t.Fatalf("Bible source calls during question rendering = %d, want 0", reader.callCount())
	}
	if store.chapterReads != 1 || store.linkReads != 1 || store.linkWrites != 0 {
		t.Fatalf("cache reads: chapter=%d link=%d writes=%d; want 1, 1 and 0",
			store.chapterReads, store.linkReads, store.linkWrites)
	}
}

func TestTelegramQuestionShowsPreparationNoticeWithoutExternalRequests(t *testing.T) {
	var pageCreations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pageCreations.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"url":"https://telegra.ph/Matfeya-4-01-01"}}`))
	}))
	defer server.Close()

	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	passageText := "Отрывок из Священного Писания."
	detail := catalog.details[100]
	detail.Questions[0].BiblePassages = []domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7, PassageText: &passageText,
	}}
	catalog.details[100] = detail

	reader := &fakeBibleChapterReader{chapter: testTelegraphChapter()}
	store := newFakeTelegraphLinkStore(true)
	publisher := newTestTelegraphPublisher("test-secret", reader, store, server)
	bot := NewBotWithChapterPublisher(api, students, catalog, learning, nil, publisher)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	last := lastEdit(t, api)
	for _, expected := range []string{
		"Задание: 1 / 2",
		"❕<b>ВОПРОС</b>",
		"• Прочтите Библейский текст:",
		"Отрывок из Священного Писания.",
		"Матфея 4:4-7",
		"Ссылка на полную главу готовится",
	} {
		if !strings.Contains(last.text, expected) {
			t.Fatalf("preparation question screen is missing %q: %s", expected, last.text)
		}
	}
	for _, row := range last.markup.InlineKeyboard {
		for _, item := range row {
			if item.URL != "" {
				t.Fatalf("URL button was included while chapter was being prepared: %+v", item)
			}
		}
	}
	if reader.callCount() != 0 || pageCreations.Load() != 0 {
		t.Fatalf("external Bible calls=%d Telegraph page creates=%d; want 0, 0",
			reader.callCount(), pageCreations.Load())
	}
	if store.chapterReads != 1 || store.linkReads != 1 || store.linkWrites != 0 {
		t.Fatalf("cache operations: chapter reads=%d link reads=%d writes=%d; want 1, 1, 0",
			store.chapterReads, store.linkReads, store.linkWrites)
	}
}
