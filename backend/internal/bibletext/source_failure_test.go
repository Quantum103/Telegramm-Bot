package bibletext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetBibleClientHandlesUnavailableAndSlowSource(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		delay     time.Duration
		clientTTL time.Duration
		wantError string
	}{
		{
			name: "service unavailable", status: http.StatusServiceUnavailable,
			wantError: "API вернул HTTP 503",
		},
		{
			name: "source timeout", delay: 100 * time.Millisecond, clientTTL: 10 * time.Millisecond,
			wantError: "превышено время ожидания HTTP-ответа",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.delay > 0 {
					select {
					case <-time.After(test.delay):
					case <-r.Context().Done():
						return
					}
				}
				if test.status != 0 {
					http.Error(w, "private upstream error body", test.status)
					return
				}
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			httpClient := server.Client()
			if test.clientTTL > 0 {
				httpClient = &http.Client{Transport: http.DefaultTransport, Timeout: test.clientTTL}
			}
			client := NewGetBibleClientWithBaseURL(server.URL, httpClient)
			_, err := client.FetchChapter(context.Background(), 40, 4)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("FetchChapter() error = %v, ожидалось сообщение %q", err, test.wantError)
			}
			if strings.Contains(err.Error(), "private upstream error body") {
				t.Fatal("ошибка раскрыла тело ответа внешнего источника")
			}
		})
	}
}
