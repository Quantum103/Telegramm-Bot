package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
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
	if len(nodes) != 3 {
		t.Fatalf("node count = %d, want translation plus two verses", len(nodes))
	}
	if nodes[0].Tag != "p" || nodes[0].Children[0] != "Синодальный перевод" {
		t.Fatalf("translation node = %+v", nodes[0])
	}
	firstVerse, ok := nodes[1].Children[0].(telegraphNode)
	if !ok || firstVerse.Tag != "strong" || firstVerse.Children[0] != "1. " {
		t.Fatalf("first verse number = %#v", nodes[1].Children[0])
	}
	if nodes[1].Children[1] != "Первый стих." || nodes[2].Children[1] != "Второй стих." {
		t.Fatalf("verse text was not preserved: %+v", nodes)
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
