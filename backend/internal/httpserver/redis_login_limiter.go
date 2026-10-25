package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const staffLoginInFlightTTL = 2 * time.Minute

var beginStaffLoginScript = redis.NewScript(`
local redis_time = redis.call('TIME')
local now = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local attempt = ARGV[1]
local ip_limit = tonumber(ARGV[2])
local ip_window_ms = tonumber(ARGV[3])
local account_limit = tonumber(ARGV[4])
local account_window_ms = tonumber(ARGV[5])
local ip_concurrency = tonumber(ARGV[6])
local account_concurrency = tonumber(ARGV[7])
local in_flight_ttl_ms = tonumber(ARGV[8])

local wait_ms = 0
local ip_failures = tonumber(redis.call('GET', KEYS[1]) or '0')
if ip_failures >= ip_limit then
  local ttl = redis.call('PTTL', KEYS[1])
  if ttl < 1 then ttl = 1000 end
  if ttl > wait_ms then wait_ms = ttl end
end
local account_failures = tonumber(redis.call('GET', KEYS[2]) or '0')
if account_failures >= account_limit then
  local ttl = redis.call('PTTL', KEYS[2])
  if ttl < 1 then ttl = 1000 end
  if ttl > wait_ms then wait_ms = ttl end
end
if wait_ms > 0 then return {0, wait_ms} end

redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', now)
if redis.call('ZCARD', KEYS[3]) >= ip_concurrency then return {0, 1000} end
if redis.call('ZCARD', KEYS[4]) >= account_concurrency then return {0, 1000} end

redis.call('ZADD', KEYS[3], now + in_flight_ttl_ms, attempt)
redis.call('ZADD', KEYS[4], now + in_flight_ttl_ms, attempt)
redis.call('PEXPIRE', KEYS[3], in_flight_ttl_ms * 2)
redis.call('PEXPIRE', KEYS[4], in_flight_ttl_ms * 2)
return {1, 0}
`)

var finishStaffLoginScript = redis.NewScript(`
local attempt = ARGV[1]
local outcome = ARGV[2]
local ip_window_ms = tonumber(ARGV[3])
local account_window_ms = tonumber(ARGV[4])

local ip_removed = redis.call('ZREM', KEYS[3], attempt)
local account_removed = redis.call('ZREM', KEYS[4], attempt)
if ip_removed + account_removed == 0 then return 0 end

if outcome == 'failure' then
  if redis.call('EXISTS', KEYS[1]) == 0 then
    redis.call('SET', KEYS[1], 1, 'PX', ip_window_ms)
  else
    redis.call('INCR', KEYS[1])
  end
  if redis.call('EXISTS', KEYS[2]) == 0 then
    redis.call('SET', KEYS[2], 1, 'PX', account_window_ms)
  else
    redis.call('INCR', KEYS[2])
  end
elseif outcome == 'success' then
  redis.call('DEL', KEYS[2])
end
return 1
`)

type RedisStaffLoginLimiter struct { client redis.UniversalClient }

func NewRedisStaffLoginLimiter(client redis.UniversalClient) *RedisStaffLoginLimiter {
	return &RedisStaffLoginLimiter{client: client}
}

func (l *RedisStaffLoginLimiter) Ping(ctx context.Context) error {
	if l == nil || l.client == nil { return errors.New("Redis login limiter client is not configured") }
	return l.client.Ping(ctx).Err()
}

func (l *RedisStaffLoginLimiter) Begin(ctx context.Context, clientIP, email string) (func(StaffLoginAttemptOutcome) error, time.Duration, bool, error) {
	if l == nil || l.client == nil { return nil, 0, false, errors.New("Redis login limiter client is not configured") }
	var randomID [18]byte
	if _, err := rand.Read(randomID[:]); err != nil { return nil, 0, false, fmt.Errorf("generate login attempt identifier: %w", err) }
	attemptID := base64.RawURLEncoding.EncodeToString(randomID[:])
	keys := redisLoginKeys(clientIP, email)
	raw, err := beginStaffLoginScript.Run(ctx, l.client, keys,
		attemptID,
		staffLoginIPFailureLimit, staffLoginIPWindow.Milliseconds(),
		staffLoginAccountFailureLimit, staffLoginAccountWindow.Milliseconds(),
		staffLoginIPConcurrencyLimit, staffLoginAccountConcurrency,
		staffLoginInFlightTTL.Milliseconds(),
	).Result()
	if err != nil { return nil, 0, false, fmt.Errorf("reserve login attempt in Redis: %w", err) }
	values, ok := raw.([]interface{})
	if !ok || len(values) != 2 { return nil, 0, false, errors.New("Redis login limiter returned malformed reservation result") }
	allowedValue, okAllowed := values[0].(int64)
	waitMS, okWait := values[1].(int64)
	if !okAllowed || !okWait || (allowedValue != 0 && allowedValue != 1) || waitMS < 0 {
		return nil, 0, false, errors.New("Redis login limiter returned invalid reservation values")
	}
	if allowedValue == 0 {
		wait := time.Duration(waitMS) * time.Millisecond
		if wait <= 0 { wait = time.Second }
		return nil, wait, false, nil
	}
	var once sync.Once
	var finishErr error
	finish := func(outcome StaffLoginAttemptOutcome) error {
		once.Do(func() {
			outcomeName := "neutral"
			switch outcome {
			case LoginOutcomeFailure: outcomeName = "failure"
			case LoginOutcomeSuccess: outcomeName = "success"
			case LoginOutcomeNeutral:
			default: finishErr = errors.New("invalid staff login attempt outcome"); return
			}
			finishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, finishErr = finishStaffLoginScript.Run(finishCtx, l.client, keys,
				attemptID, outcomeName, staffLoginIPWindow.Milliseconds(), staffLoginAccountWindow.Milliseconds(),
			).Result()
			if finishErr != nil { finishErr = fmt.Errorf("finish login attempt in Redis: %w", finishErr) }
		})
		return finishErr
	}
	return finish, 0, true, nil
}

func redisLoginKeys(clientIP, email string) []string {
	keys := loginRateKeys(clientIP, email)
	ipBase := "staff-login:v1:" + keys[0].value
	accountBase := "staff-login:v1:" + keys[1].value
	return []string{ipBase + ":failures", accountBase + ":failures", ipBase + ":inflight", accountBase + ":inflight"}
}
