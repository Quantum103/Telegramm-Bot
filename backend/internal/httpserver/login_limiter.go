package httpserver

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	staffLoginIPFailureLimit       = 10
	staffLoginIPWindow             = 5 * time.Minute
	staffLoginIPConcurrencyLimit   = 4
	staffLoginAccountFailureLimit  = 5
	staffLoginAccountWindow        = 15 * time.Minute
	staffLoginAccountConcurrency   = 2
	staffLoginLimiterMaxEntries    = 8192
	staffLoginConcurrentRetryDelay = time.Second
)

type loginAttemptOutcome uint8

const (
	loginOutcomeNeutral loginAttemptOutcome = iota
	loginOutcomeFailure
	loginOutcomeSuccess
)

type loginRatePolicy struct {
	window                 time.Duration
	failureLimit           int
	concurrencyLimit       int
	resetFailuresOnSuccess bool
}

type loginRateKey struct {
	value  string
	policy loginRatePolicy
}

type loginRateEntry struct {
	key           string
	policy        loginRatePolicy
	failures      int
	windowStarted time.Time
	lastSeen      time.Time
	inFlight      int
}

type staffLoginLimiter struct {
	mu         sync.Mutex
	now        func() time.Time
	maxEntries int
	entries    map[string]*list.Element
	lru        *list.List
}

func newStaffLoginLimiter() *staffLoginLimiter {
	return &staffLoginLimiter{
		now:        time.Now,
		maxEntries: staffLoginLimiterMaxEntries,
		entries:    make(map[string]*list.Element),
		lru:        list.New(),
	}
}

// begin checks the IP and IP/account limits atomically and reserves an in-flight slot.
// finish must be called exactly once with the outcome of the authentication attempt.
func (l *staffLoginLimiter) begin(clientIP, email string) (func(loginAttemptOutcome), time.Duration, bool) {
	if l == nil {
		return nil, time.Second, false
	}
	keys := loginRateKeys(clientIP, email)

	l.mu.Lock()
	now := l.now()
	ipEntry := l.entryFor(keys[0], now)
	if ipEntry == nil {
		l.mu.Unlock()
		return nil, staffLoginConcurrentRetryDelay, false
	}
	if wait := l.retryIfBlocked(ipEntry, now); wait > 0 {
		l.mu.Unlock()
		return nil, wait, false
	}

	accountEntry := l.entryFor(keys[1], now)
	if accountEntry == nil {
		l.mu.Unlock()
		return nil, staffLoginConcurrentRetryDelay, false
	}
	if wait := l.retryIfBlocked(accountEntry, now); wait > 0 {
		l.mu.Unlock()
		return nil, wait, false
	}

	ipEntry.inFlight++
	accountEntry.inFlight++
	l.mu.Unlock()

	var once sync.Once
	finish := func(outcome loginAttemptOutcome) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			finishedAt := l.now()
			for _, key := range keys {
				element, exists := l.entries[key.value]
				if !exists {
					continue
				}
				entry := element.Value.(*loginRateEntry)
				if entry.inFlight > 0 {
					entry.inFlight--
				}
				if !finishedAt.Before(entry.windowStarted.Add(entry.policy.window)) {
					entry.failures = 0
					entry.windowStarted = finishedAt
				}
				switch outcome {
				case loginOutcomeFailure:
					if entry.failures == 0 {
						entry.windowStarted = finishedAt
					}
					entry.failures++
				case loginOutcomeSuccess:
					if entry.policy.resetFailuresOnSuccess {
						entry.failures = 0
						entry.windowStarted = finishedAt
					}
				}
				entry.lastSeen = finishedAt
				l.lru.MoveToFront(element)
			}
		})
	}
	return finish, 0, true
}

func loginRateKeys(clientIP, email string) [2]loginRateKey {
	clientIP = strings.TrimSpace(clientIP)
	if clientIP == "" {
		clientIP = "unknown"
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	if normalizedEmail == "" || len(normalizedEmail) > 254 {
		normalizedEmail = "<invalid-email>"
	}

	return [2]loginRateKey{
		{
			value: "ip:" + rateKeyDigest("ip\x00"+clientIP),
			policy: loginRatePolicy{
				window: staffLoginIPWindow, failureLimit: staffLoginIPFailureLimit,
				concurrencyLimit: staffLoginIPConcurrencyLimit,
			},
		},
		{
			value: "account:" + rateKeyDigest("account\x00"+clientIP+"\x00"+normalizedEmail),
			policy: loginRatePolicy{
				window: staffLoginAccountWindow, failureLimit: staffLoginAccountFailureLimit,
				concurrencyLimit: staffLoginAccountConcurrency, resetFailuresOnSuccess: true,
			},
		},
	}
}

func rateKeyDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (l *staffLoginLimiter) entryFor(key loginRateKey, now time.Time) *loginRateEntry {
	if element, exists := l.entries[key.value]; exists {
		entry := element.Value.(*loginRateEntry)
		if !now.Before(entry.windowStarted.Add(entry.policy.window)) {
			entry.failures = 0
			entry.windowStarted = now
		}
		entry.lastSeen = now
		l.lru.MoveToFront(element)
		return entry
	}

	if l.maxEntries < 2 {
		l.maxEntries = 2
	}
	for len(l.entries) >= l.maxEntries {
		if !l.evictOldestIdle() {
			return nil
		}
	}
	entry := &loginRateEntry{
		key: key.value, policy: key.policy,
		windowStarted: now, lastSeen: now,
	}
	l.entries[key.value] = l.lru.PushFront(entry)
	return entry
}

func (l *staffLoginLimiter) retryIfBlocked(entry *loginRateEntry, now time.Time) time.Duration {
	if entry.failures >= entry.policy.failureLimit {
		remaining := entry.windowStarted.Add(entry.policy.window).Sub(now)
		if remaining > 0 {
			return remaining
		}
	}
	if entry.inFlight >= entry.policy.concurrencyLimit {
		return staffLoginConcurrentRetryDelay
	}
	return 0
}

func (l *staffLoginLimiter) evictOldestIdle() bool {
	for element := l.lru.Back(); element != nil; element = element.Prev() {
		entry := element.Value.(*loginRateEntry)
		if entry.inFlight != 0 {
			continue
		}
		delete(l.entries, entry.key)
		l.lru.Remove(element)
		return true
	}
	return false
}

// loginClientIP uses X-Real-IP set by the project's Nginx proxy, which overwrites
// this header. Keep the backend reachable only through trusted proxies in deployments
// where client-supplied forwarding headers could otherwise be accepted.
func loginClientIP(r *http.Request) string {
	if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); forwarded != nil {
		return forwarded.String()
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(remote); ip != nil {
		return ip.String()
	}
	return "unknown"
}

func formatRetryAfter(wait time.Duration) string {
	seconds := int(wait / time.Second)
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
