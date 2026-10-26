package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersApplyToSuccessAndErrorResponses(t *testing.T) {
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
		"Content-Security-Policy": "default-src 'none'; base-uri 'none'; frame-ancestors 'none'",
	}

	for _, path := range []string{"/health/live", "/not-found"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			NewHandler(fakePinger{}, testLogger()).ServeHTTP(recorder, request)

			for name, value := range want {
				if got := recorder.Header().Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}
