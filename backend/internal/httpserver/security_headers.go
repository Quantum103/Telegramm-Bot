package httpserver

import "net/http"

// securityHeaders adds response headers suitable for this JSON API. HSTS is
// intentionally configured at the TLS-terminating edge, where HTTPS is known.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		header.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")

		next.ServeHTTP(w, r)
	})
}
