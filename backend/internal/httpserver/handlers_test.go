package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct {
	err error
}

func (p fakePinger) Ping(context.Context) error {
	return p.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLiveReturnsOK(t *testing.T) {
	server := httptest.NewServer(NewHandler(fakePinger{}, testLogger()))
	defer server.Close()

	response, err := http.Get(server.URL + "/health/live")
	if err != nil {
		t.Fatalf("GET /health/live: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if string(body) != "{\"status\":\"ok\"}\n" {
		t.Errorf("body = %q", body)
	}
}

func TestReadyChecksDatabase(t *testing.T) {
	tests := []struct {
		name       string
		pinger     databasePinger
		wantStatus int
		wantBody   string
	}{
		{name: "database available", pinger: fakePinger{}, wantStatus: http.StatusOK, wantBody: "{\"status\":\"ok\"}\n"},
		{name: "database error", pinger: fakePinger{err: errors.New("private database details")}, wantStatus: http.StatusServiceUnavailable, wantBody: "{\"status\":\"unavailable\"}\n"},
		{name: "database missing", pinger: nil, wantStatus: http.StatusServiceUnavailable, wantBody: "{\"status\":\"unavailable\"}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
			recorder := httptest.NewRecorder()
			NewHandler(tt.pinger, testLogger()).ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if recorder.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", recorder.Body.String(), tt.wantBody)
			}
			if strings.Contains(recorder.Body.String(), "private database details") {
				t.Error("response leaked internal database error")
			}
		})
	}
}

func TestUnknownRouteReturnsNotFound(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	recorder := httptest.NewRecorder()
	NewHandler(fakePinger{}, testLogger()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
