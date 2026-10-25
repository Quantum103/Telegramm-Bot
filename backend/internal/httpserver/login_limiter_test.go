package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newClockedLoginLimiter(now *time.Time) *staffLoginLimiter {
	limiter := newStaffLoginLimiter()
	limiter.now = func() time.Time { return *now }
	return limiter
}

func failedLoginAttempt(t *testing.T, limiter *staffLoginLimiter, ip, email string) (bool, time.Duration) {
	t.Helper()
	finish, retryAfter, allowed := limiter.begin(ip, email)
	if allowed {
		finish(loginOutcomeFailure)
	}
	return allowed, retryAfter
}

func TestStaffLoginLimiterBlocksRepeatedFailuresForAccountAndIP(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	limiter := newClockedLoginLimiter(&now)

	for attempt := 0; attempt < staffLoginAccountFailureLimit; attempt++ {
		allowed, _ := failedLoginAttempt(t, limiter, "192.0.2.10", "admin@example.test")
		if !allowed {
			t.Fatalf("attempt %d unexpectedly blocked", attempt+1)
		}
	}
	allowed, wait := failedLoginAttempt(t, limiter, "192.0.2.10", "admin@example.test")
	if allowed || wait <= 0 {
		t.Fatalf("next account/IP attempt = allowed %v, wait %s; want blocked with positive wait", allowed, wait)
	}

	allowed, _ = failedLoginAttempt(t, limiter, "192.0.2.11", "admin@example.test")
	if !allowed {
		t.Fatal("a different IP was unexpectedly blocked by the account/IP bucket")
	}

	now = now.Add(staffLoginAccountWindow)
	allowed, _ = failedLoginAttempt(t, limiter, "192.0.2.10", "admin@example.test")
	if !allowed {
		t.Fatal("attempt was not allowed after account/IP window expired")
	}
}

func TestStaffLoginLimiterBlocksIPAcrossDifferentAccounts(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	limiter := newClockedLoginLimiter(&now)

	for attempt := 0; attempt < staffLoginIPFailureLimit; attempt++ {
		allowed, _ := failedLoginAttempt(t, limiter, "192.0.2.20", fmt.Sprintf("user-%d@example.test", attempt))
		if !allowed {
			t.Fatalf("attempt %d unexpectedly blocked before IP limit", attempt+1)
		}
	}
	allowed, wait := failedLoginAttempt(t, limiter, "192.0.2.20", "another-user@example.test")
	if allowed || wait <= 0 {
		t.Fatalf("IP should be blocked across accounts; got allowed=%v wait=%s", allowed, wait)
	}
}

func TestSuccessfulLoginResetsOnlyAccountIPFailures(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	limiter := newClockedLoginLimiter(&now)

	for attempt := 0; attempt < staffLoginAccountFailureLimit-1; attempt++ {
		allowed, _ := failedLoginAttempt(t, limiter, "192.0.2.30", "staff@example.test")
		if !allowed {
			t.Fatalf("failure attempt %d unexpectedly blocked", attempt+1)
		}
	}

	finish, _, allowed := limiter.begin("192.0.2.30", "staff@example.test")
	if !allowed {
		t.Fatal("valid login attempt unexpectedly blocked before limit")
	}
	finish(loginOutcomeSuccess)

	for attempt := 0; attempt < staffLoginAccountFailureLimit; attempt++ {
		allowed, _ := failedLoginAttempt(t, limiter, "192.0.2.30", "staff@example.test")
		if !allowed {
			t.Fatalf("account/IP counter was not reset after success at attempt %d", attempt+1)
		}
	}
	_, _, allowed = limiter.begin("192.0.2.30", "staff@example.test")
	if allowed {
		t.Fatal("account/IP bucket should be blocked after threshold failures following success")
	}
}

func TestStaffLoginLimiterBoundsConcurrentAttempts(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	limiter := newClockedLoginLimiter(&now)
	finishers := make([]func(loginAttemptOutcome), 0, staffLoginAccountConcurrency)
	for attempt := 0; attempt < staffLoginAccountConcurrency; attempt++ {
		finish, _, allowed := limiter.begin("192.0.2.40", "admin@example.test")
		if !allowed {
			t.Fatalf("concurrent attempt %d unexpectedly blocked", attempt+1)
		}
		finishers = append(finishers, finish)
	}
	_, wait, allowed := limiter.begin("192.0.2.40", "admin@example.test")
	if allowed || wait <= 0 {
		t.Fatalf("over-concurrent attempt should be blocked, got allowed=%v wait=%s", allowed, wait)
	}
	for _, finish := range finishers {
		finish(loginOutcomeNeutral)
	}
}

func TestStaffLoginLimiterMemoryIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	limiter := newClockedLoginLimiter(&now)
	limiter.maxEntries = 4
	for i := 0; i < 20; i++ {
		finish, _, allowed := limiter.begin(fmt.Sprintf("192.0.2.%d", i+1), fmt.Sprintf("user-%d@example.test", i))
		if !allowed {
			t.Fatalf("attempt %d was unexpectedly blocked while idle entries could be evicted", i)
		}
		finish(loginOutcomeNeutral)
		if len(limiter.entries) > limiter.maxEntries {
			t.Fatalf("limiter entries = %d, max = %d", len(limiter.entries), limiter.maxEntries)
		}
	}
}

func TestLoginClientIPPrefersValidProxyHeaderAndFallsBackSafely(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/auth/login", nil)
	request.RemoteAddr = "192.0.2.5:1234"
	request.Header.Set("X-Real-IP", "2001:db8::7")
	if got := loginClientIP(request); got != "2001:db8::7" {
		t.Fatalf("loginClientIP() = %q, want forwarded IPv6", got)
	}
	request.Header.Set("X-Real-IP", "not-an-ip")
	if got := loginClientIP(request); got != "192.0.2.5" {
		t.Fatalf("loginClientIP() = %q, want remote IPv4 fallback", got)
	}
}
