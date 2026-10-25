package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func integrationRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if addr == "" { t.Skip("TEST_REDIS_ADDR is not set; skipping Redis integration test") }
	client := redis.NewClient(&redis.Options{
		Addr: addr, Password: os.Getenv("TEST_REDIS_PASSWORD"), DB: 0,
		DialTimeout: 2*time.Second, ReadTimeout: 2*time.Second, WriteTimeout: 2*time.Second,
		MaxRetries: -1, PoolSize: 4,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second); defer cancel()
	if err := client.Ping(ctx).Err(); err != nil { client.Close(); t.Fatalf("ping test Redis: %v", err) }
	t.Cleanup(func(){ _=client.Close() })
	return client
}
func uniqueLimiterIdentity(t *testing.T) (string,string) {
	t.Helper()
	return "198.51.100.77",fmt.Sprintf("test-%d-%s@example.test",time.Now().UnixNano(),strings.ReplaceAll(t.Name(),"/","-"))
}
func TestRedisStaffLoginLimiterSharesFailureCountsAcrossClients(t *testing.T) {
	clientA,clientB:=integrationRedisClient(t),integrationRedisClient(t)
	limiterA,limiterB:=NewRedisStaffLoginLimiter(clientA),NewRedisStaffLoginLimiter(clientB)
	ip,email:=uniqueLimiterIdentity(t)
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
	for attempt:=0;attempt<staffLoginAccountFailureLimit;attempt++ {
		finish,_,allowed,err:=limiterA.Begin(ctx,ip,email)
		if err!=nil||!allowed {t.Fatalf("attempt %d begin: allowed=%v err=%v",attempt+1,allowed,err)}
		if err:=finish(LoginOutcomeFailure);err!=nil {t.Fatalf("attempt %d finish: %v",attempt+1,err)}
	}
	_,wait,allowed,err:=limiterB.Begin(ctx,ip,email)
	if err!=nil||allowed||wait<=0 {t.Fatalf("separate client should observe shared block: allowed=%v wait=%s err=%v",allowed,wait,err)}
}
func TestRedisStaffLoginLimiterSharesConcurrencyReservations(t *testing.T) {
	clientA,clientB:=integrationRedisClient(t),integrationRedisClient(t)
	limiterA,limiterB:=NewRedisStaffLoginLimiter(clientA),NewRedisStaffLoginLimiter(clientB)
	ip,email:=uniqueLimiterIdentity(t)
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
	finishA,_,allowed,err:=limiterA.Begin(ctx,ip,email)
	if err!=nil||!allowed {t.Fatalf("first reservation: allowed=%v err=%v",allowed,err)}
	finishB,_,allowed,err:=limiterB.Begin(ctx,ip,email)
	if err!=nil||!allowed {_=finishA(LoginOutcomeNeutral);t.Fatalf("second reservation: allowed=%v err=%v",allowed,err)}
	_,wait,allowed,err:=limiterB.Begin(ctx,ip,email)
	if err!=nil||allowed||wait<=0 {_=finishA(LoginOutcomeNeutral);_=finishB(LoginOutcomeNeutral);t.Fatalf("third reservation should be blocked: allowed=%v wait=%s err=%v",allowed,wait,err)}
	if err:=finishA(LoginOutcomeNeutral);err!=nil {t.Fatalf("release first reservation: %v",err)}
	finishC,_,allowed,err:=limiterB.Begin(ctx,ip,email)
	if err!=nil||!allowed {_=finishB(LoginOutcomeNeutral);t.Fatalf("reservation should succeed after release: allowed=%v err=%v",allowed,err)}
	if err:=finishB(LoginOutcomeNeutral);err!=nil {t.Fatalf("release second reservation: %v",err)}
	if err:=finishC(LoginOutcomeNeutral);err!=nil {t.Fatalf("release third reservation: %v",err)}
}
func TestRedisLoginKeysDoNotExposeIPOrEmail(t *testing.T) {
	ip,email:="203.0.113.123","private-admin@example.test"
	for _,key:=range redisLoginKeys(ip,email) {
		if strings.Contains(key,ip)||strings.Contains(key,email) {t.Fatalf("Redis key contains identifying data: %q",key)}
	}
}

func TestRedisLimiterFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond,
		ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond,
		MaxRetries: -1,
	})
	defer client.Close()
	limiter := NewRedisStaffLoginLimiter(client)
	api := &fakeStaffAuthAPI{loginErr: nil}
	handler := NewHandlerWithStaffAuthAndLoginLimiter(fakePinger{}, testLogger(), nil, api, limiter, true)
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/auth/login",
		strings.NewReader(`{"email":"admin@example.test","password":"some-long-password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://admin.example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if api.loginCalls != 0 {
		t.Fatalf("authentication service called %d times while Redis was unavailable", api.loginCalls)
	}
}
