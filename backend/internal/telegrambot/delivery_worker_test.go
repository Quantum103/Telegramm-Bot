package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeConversationDeliveryQueue struct {
	sent       bool
	failed     bool
	retry      bool
	errorCode  string
	retryDelay time.Duration
}

func (f *fakeConversationDeliveryQueue) ClaimDueDelivery(context.Context, time.Duration) (domain.ConversationDelivery, error) {
	return domain.ConversationDelivery{}, domain.ErrNotFound
}
func (f *fakeConversationDeliveryQueue) MarkDeliverySent(context.Context, int64) error {
	f.sent = true
	return nil
}
func (f *fakeConversationDeliveryQueue) ScheduleDeliveryRetry(_ context.Context, _ int64, code string, delay time.Duration) error {
	f.retry, f.errorCode, f.retryDelay = true, code, delay
	return nil
}
func (f *fakeConversationDeliveryQueue) MarkDeliveryFailed(_ context.Context, _ int64, code string) error {
	f.failed, f.errorCode = true, code
	return nil
}

func TestDeliveryWorkerUsesTelegramAPIAndMarksSuccessfulDelivery(t *testing.T) {
	var received struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botfake-token/sendMessage" {
			t.Errorf("unexpected Telegram endpoint: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode sendMessage request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":91}}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 1, MessageID: 2, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.sent || queue.failed || queue.retry {
		t.Fatalf("неверный статус успешной отправки: %+v", queue)
	}
	if received.ChatID != 123456 || received.Text != "Ответ куратора" {
		t.Fatalf("неверное содержимое sendMessage: %+v", received)
	}
}

func TestDeliveryWorkerMarksBlockedBotAsPermanentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 2, MessageID: 3, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.failed || queue.retry || queue.sent || queue.errorCode != "telegram_recipient_unavailable" {
		t.Fatalf("блокировка Telegram-бота не обработана как окончательная ошибка: %+v", queue)
	}
}

func TestDeliveryWorkerMarksMissingTelegramChatAsPermanentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 20, MessageID: 30, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.failed || queue.retry || queue.sent || queue.errorCode != "telegram_recipient_unavailable" {
		t.Fatalf("отсутствующий/не начатый Telegram-чат не обработан как окончательная ошибка: %+v", queue)
	}
}

func TestDeliveryWorkerHonorsTelegramRateLimitRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":19}}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 3, MessageID: 4, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.retry || queue.failed || queue.sent || queue.errorCode != "telegram_rate_limited" || queue.retryDelay != 19*time.Second {
		t.Fatalf("неверная обработка 429/retry_after: %+v", queue)
	}
}

func TestDeliveryWorkerRetriesTelegramServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error"}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 4, MessageID: 5, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.retry || queue.failed || queue.sent || queue.errorCode != "telegram_server_error" || queue.retryDelay != 5*time.Second {
		t.Fatalf("неверная обработка временной ошибки Telegram: %+v", queue)
	}
}

func TestDeliveryWorkerRetriesUnknownTransportResult(t *testing.T) {
	client := &http.Client{Transport: workerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated network timeout")
	})}
	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", "http://telegram.test", client)
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 5, MessageID: 6, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.retry || queue.failed || queue.sent || queue.errorCode != "delivery_outcome_unknown" {
		t.Fatalf("неизвестный исход запроса не запланирован на повтор: %+v", queue)
	}
}

func TestDeliveryWorkerSendsCuratorNotificationWithConversationButton(t *testing.T) {
	var payload struct {
		ChatID      int64  `json:"chat_id"`
sh: locale: not found
The files belonging to this database system will be owned by user "postgres".
2026-11-01 09:50:07.399 UTC [40] WARNING:  no usable system locales were found
initdb: warning: enabling "trust" authentication for local connections
initdb: hint: You can change this by editing pg_hba.conf or using the option -A, or --auth-local and --auth-host, the next time you run initdb.
2026-11-01 09:50:08.323 UTC [1] LOG:  starting PostgreSQL 18.6 on x86_64-pc-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit
2026-11-01 09:50:08.323 UTC [1] LOG:  listening on IPv4 address "0.0.0.0", port 5432
2026-11-01 09:50:08.323 UTC [1] LOG:  listening on IPv6 address "::", port 5432
2026-11-01 09:50:08.324 UTC [1] LOG:  listening on Unix socket "/var/run/postgresql/.s.PGSQL.5432"
2026-11-01 09:50:08.329 UTC [68] LOG:  database system was shut down at 2026-11-01 09:50:08 UTC
2026-11-01 09:50:08.334 UTC [1] LOG:  database system is ready to accept connections
This user must also own the server process.

The database cluster will be initialized with locale "en_US.utf8".
The default database encoding has accordingly been set to "UTF8".
The default text search configuration will be set to "english".

Data page checksums are enabled.

fixing permissions on existing directory /var/lib/postgresql/18/docker ... ok
creating subdirectories ... ok
selecting dynamic shared memory implementation ... posix
selecting default "max_connections" ... 100
selecting default "shared_buffers" ... 128MB
selecting default time zone ... UTC
creating configuration files ... ok
running bootstrap script ... ok
performing post-bootstrap initialization ... ok
syncing data to disk ... ok


Success. You can now start the database server using:

    pg_ctl -D /var/lib/postgresql/18/docker -l logfile start

waiting for server to start....2026-11-01 09:50:08.035 UTC [46] LOG:  starting PostgreSQL 18.6 on x86_64-pc-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit
2026-11-01 09:50:08.035 UTC [46] LOG:  listening on Unix socket "/var/run/postgresql/.s.PGSQL.5432"
2026-11-01 09:50:08.038 UTC [52] LOG:  database system was shut down at 2026-11-01 09:50:07 UTC
2026-11-01 09:50:08.041 UTC [46] LOG:  database system is ready to accept connections
 done
server started
CREATE DATABASE


/usr/local/bin/docker-entrypoint.sh: ignoring /docker-entrypoint-initdb.d/*

waiting for server to shut down....2026-11-01 09:50:08.197 UTC [46] LOG:  received fast shutdown request
2026-11-01 09:50:08.198 UTC [46] LOG:  aborting any active transactions
2026-11-01 09:50:08.201 UTC [46] LOG:  background worker "logical replication launcher" (PID 55) exited with exit code 1
2026-11-01 09:50:08.203 UTC [50] LOG:  shutting down
2026-11-01 09:50:08.204 UTC [50] LOG:  checkpoint starting: shutdown immediate
2026-11-01 09:50:08.256 UTC [50] LOG:  checkpoint complete: wrote 943 buffers (5.8%), wrote 3 SLRU buffers; 0 WAL file(s) added, 0 removed, 0 recycled; write=0.048 s, sync=0.004 s, total=0.053 s; sync files=303, longest=0.002 s, average=0.001 s; distance=4362 kB, estimate=4362 kB; lsn=0/1BA8870, redo lsn=0/1BA8870
2026-11-01 09:50:08.269 UTC [46] LOG:  database system is shut down
 done
server stopped

PostgreSQL init process complete; ready for start up.

Starting Redis Server
1:C 01 Nov 2026 09:50:09.932 # WARNING Memory overcommit must be enabled! Without it, a background save or replication may fail under low memory condition. Being disabled, it can also cause failures without low memory condition, see https://github.com/jemalloc/jemalloc/issues/1328. To fix this issue add 'vm.overcommit_memory = 1' to /etc/sysctl.conf and then reboot or run the command 'sysctl vm.overcommit_memory=1' for this to take effect.
1:C 01 Nov 2026 09:50:09.932 * oO0OoO0OoO0Oo Redis is starting oO0OoO0OoO0Oo
1:C 01 Nov 2026 09:50:09.932 * Redis version=8.10.3, bits=64, commit=00000000, modified=1, pid=1, just started
1:C 01 Nov 2026 09:50:09.932 * Configuration loaded
1:M 01 Nov 2026 09:50:09.932 * monotonic clock: POSIX clock_gettime
1:M 01 Nov 2026 09:50:09.933 * Running mode=standalone, port=6379.
1:M 01 Nov 2026 09:50:09.934 * <bf> RedisBloom version 8.10.1 (Git=unknown)
1:M 01 Nov 2026 09:50:09.934 * <bf> Registering configuration options: [
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ bf-error-rate       :      0.01 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ bf-initial-size     :       100 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ bf-expansion-factor :         2 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ cf-bucket-size      :         2 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ cf-initial-size     :      1024 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ cf-max-iterations   :        20 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ cf-expansion-factor :         1 }
1:M 01 Nov 2026 09:50:09.934 * <bf> 	{ cf-max-expansions   :        32 }
1:M 01 Nov 2026 09:50:09.934 * <bf> ]
1:M 01 Nov 2026 09:50:09.934 * Module 'bf' loaded from /usr/local/lib/redis/modules//redisbloom.so
1:M 01 Nov 2026 09:50:09.938 * <search> search-workers default: 4 (min of MAX_WORKER_THREADS=16 and CPU cores)
1:M 01 Nov 2026 09:50:09.938 * <search> Redis version found by RedisSearch : 8.10.3 - oss
1:M 01 Nov 2026 09:50:09.938 * <search> RediSearch version 8.10.1 (Git=)
1:M 01 Nov 2026 09:50:09.938 * <search> gc: ON, prefix min length: 2, min word length to stem: 4, prefix max expansions: 200, query timeout (ms): 500, timeout policy: return, oom policy: return, cursor read size: 1000, cursor max idle (ms): 300000, max doctable size: 1000000, max number of search results:  1000000, default scorer: BM25STD, 
1:M 01 Nov 2026 09:50:09.938 * <search> Initialized thread pools!
1:M 01 Nov 2026 09:50:09.938 * <search> Enabled workers threadpool of size 4
1:M 01 Nov 2026 09:50:09.938 * <search> Subscribe to config changes
1:M 01 Nov 2026 09:50:09.938 * <search> Subscribe to cluster slot migration events
1:M 01 Nov 2026 09:50:09.938 * <search> Subscribe to cluster topology change events
1:M 01 Nov 2026 09:50:09.938 * <search> Enabled role change notification
1:M 01 Nov 2026 09:50:09.939 * <search> Cluster configuration: AUTO partitions, type: 0, coordinator timeout: 0ms
1:M 01 Nov 2026 09:50:09.939 * Module 'search' loaded from /usr/local/lib/redis/modules//redisearch.so
1:M 01 Nov 2026 09:50:09.940 * <timeseries> RedisTimeSeries version 81001, git_sha=
1:M 01 Nov 2026 09:50:09.940 * <timeseries> Redis version found by RedisTimeSeries : 8.10.3 - oss
1:M 01 Nov 2026 09:50:09.940 * <timeseries> Registering configuration options: [
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-compaction-policy   :              }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-num-threads         :            3 }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-libmr-protocol      :     INTERNAL }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-retention-policy    :            0 }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-duplicate-policy    :        block }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-chunk-size-bytes    :         4096 }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-encoding            :   compressed }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-ignore-max-time-diff:            0 }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-ignore-max-val-diff :     0.000000 }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> 	{ ts-topology-events     :         true }
1:M 01 Nov 2026 09:50:09.940 * <timeseries> ]
1:M 01 Nov 2026 09:50:09.940 * <timeseries> Detected redis oss (cluster-enabled=no)
1:M 01 Nov 2026 09:50:09.940 * <timeseries> Subscribe to ASM events
1:M 01 Nov 2026 09:50:09.940 * <timeseries> Subscribe to topology changes events
1:M 01 Nov 2026 09:50:09.940 * <timeseries> Enabled diskless replication
1:M 01 Nov 2026 09:50:09.940 * Module 'timeseries' loaded from /usr/local/lib/redis/modules//redistimeseries.so
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Created new data type 'ReJSON-RL'
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> version: 81000 git sha: unknown branch: unknown
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V1 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V2 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V3 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V4 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V5 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V6 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V7 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Exported RedisJSON_V8 API
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Enabled diskless replication
1:M 01 Nov 2026 09:50:09.941 * <ReJSON> Initialized shared string cache, thread safe: true.
1:M 01 Nov 2026 09:50:09.941 * Module 'ReJSON' loaded from /usr/local/lib/redis/modules//rejson.so
1:M 01 Nov 2026 09:50:09.941 * <search> Acquired RedisJSON_V8 API
1:M 01 Nov 2026 09:50:09.941 * Server initialized
1:M 01 Nov 2026 09:50:09.942 * Ready to accept connections tcp
1:M 01 Nov 2026 09:50:09.942 # WARNING: Redis does not require authentication and is not protected by network restrictions. Redis will accept connections from any IP address on any network interface.