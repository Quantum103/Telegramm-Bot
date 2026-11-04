# Этап 7.3. Нагрузочные и отказоустойчивые проверки

Дата: 2026-11-04  
Ветка: `feature/stage-7-final-audit`  
Commit с результатами CI: `32e4061ec287f882a3c12bcd1e6f960162d2a272`  
GitHub Actions run №825: https://github.com/Quantum103/Telegramm-Bot/actions/runs/41833829982

## 1. Результат

Добавлены изолированные тестовые профили для трёх уровней:

1. Прямые операции навигации в PostgreSQL для 1, 5 и 10 одновременных учеников.
2. Очередь Telegram updates (4 worker, ёмкость каждой очереди 16) для 1, 5 и 10 учеников.
3. HTTP-клиент Telegram Bot API через локальный fake API с 1 ms задержкой для 1, 5 и 10 учеников.

Дополнительно проверены ошибки Telegram API (429, 500 и timeout), недоступность/timeout источника Библии, а также измерение SQL-задержки через pgx tracer.

**CI run №825 завершился success.** Успешны обе job: Go tests + PostgreSQL migrations и frontend + Compose validation. В backend job прошли миграции, gofmt, `go test ./...`, все три нагрузочных шага, race detector, прежние benchmarks и `go vet ./...`.

Это измерения тестовых слоёв на GitHub Actions runner, а не обещание, что production-бот выдержит такое же число реальных пользователей. Полная end-to-end нагрузка, CPU/RAM production-контейнера и отказ реального сетевого соединения PostgreSQL в этом подэтапе не измерялись.

## 2. PostgreSQL: навигация 1/5/10 учеников

Профиль: `TestQuestionNavigationLoadProfiles`, тестовый PostgreSQL 18 из CI, пул `MaxConns=10`, `MinConns=1`. Каждый виртуальный ученик выполнял 1000 последовательных переходов между двумя опубликованными тестовыми вопросами. Поэтому выборка составляет 1 000 / 5 000 / 10 000 завершённых операций. Измеряется длительность repository-операции `NavigateQuestion`, без Telegram HTTP и без отрисовки сообщения.

| Одновременных учеников | Операций | Throughput, op/s | p50, ms | p95, ms | p99, ms | SQL-команд на операцию | SQL p99, ms | Peak acquired connections |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1 000 | 437.91 | 2.254 | 2.457 | 3.024 | 6 | 0.640 | 1 |
| 5 | 5 000 | 1 042.38 | 4.656 | 6.061 | 7.613 | 6 | 1.965 | 5 |
| 10 | 10 000 | 1 115.52 | 8.717 | 12.157 | 14.658 | 6 | 4.278 | 10 |

Дополнительные наблюдения пула:

| Ученики | Acquire count | Суммарное ожидание acquire, ms | Empty acquire count | Пиковое число соединений |
|---:|---:|---:|---:|---:|
| 1 | 1 000 | 0.174 | 0 | 2 |
| 5 | 5 000 | 28.567 | 3 | 5 |
| 10 | 10 000 | 92.673 | 5 | 10 |

Pool metrics снимаются во время теста через `pgxpool.Stat()`; пики соединений — периодически, с интервалом 5 ms. Суммарное ожидание acquire — накопленная длительность всех ожиданий в пуле, а не задержка одной операции.

**Что видно:** при увеличении параллельности p99 repository-навигации закономерно растёт: с 3.02 ms на одном ученике до 14.66 ms на десяти. В десятипользовательском профиле пул доходил до настроенного лимита 10 соединений; появились 5 empty-acquire событий и суммарно 92.67 ms ожидания получения соединений. Это основание отдельно исследовать реальную конкуренцию транзакций и баланс пула, но не основание автоматически увеличивать `DATABASE_MAX_CONNS`: в production пул общий для API, Telegram и фоновых workers.

На каждой операции зафиксировано 6 SQL-команд. SQL p99 в профиле 10 учеников составил 4.278 ms.

### Проверка инструмента SQL latency

Перед профилями тест выполняет `SELECT pg_sleep(0.03)`. Tracer зафиксировал 30.612 ms. Это подтверждает, что измерение захватывает задержку выполнения SQL, а не только время подготовки вызова.

## 3. Telegram update worker pool

Профиль: `TestUpdateWorkerPoolLoadProfiles`. Реальный `UpdateWorkerPool` приложения, 4 worker и ёмкость 16 updates на worker. 1, 5 и 10 виртуальных учеников отправляли по 100 updates; handler искусственно работал 1 ms. Измеренная задержка включает очередь и обработчик, но не PostgreSQL и Telegram API.

| Одновременных учеников | Updates | Throughput, updates/s | p50, ms | p95, ms | p99, ms |
|---:|---:|---:|---:|---:|---:|
| 1 | 100 | 923.18 | 1.081 | 1.099 | 1.121 |
| 5 | 500 | 2 293.65 | 1.102 | 2.182 | 2.317 |
| 10 | 1 000 | 3 073.24 | 3.229 | 3.270 | 3.296 |

Это синтетический профиль механизма очередей. Handler не обращается к БД и не вызывает Telegram API, а каждое виртуальное лицо посылает следующий update после завершения предыдущего. Эти числа нельзя трактовать как throughput всего бота.

## 4. Telegram Bot API: fake HTTP server

Профиль: `TestTelegramAPILoadProfiles`. Каждый виртуальный ученик выполнял 100 `sendMessage` запросов к локальному `httptest.Server`; fake endpoint добавлял 1 ms искусственной задержки.

| Одновременных учеников | HTTP-запросов | Throughput, requests/s | p50, ms | p95, ms | p99, ms |
|---:|---:|---:|---:|---:|---:|
| 1 | 100 | 783.55 | 1.250 | 1.447 | 1.748 |
| 5 | 500 | 3 759.26 | 1.311 | 1.446 | 1.885 |
| 10 | 1 000 | 6 941.10 | 1.414 | 1.703 | 2.148 |

Здесь измеряется клиентский HTTP round trip к локальной тестовой сети. Это не latency Telegram в интернете и не лимит Bot API. Не испытывалась фактическая квота Telegram.

## 5. Отказоустойчивость

| Компонент | Сценарий | Результат |
|---|---|---|
| Telegram Bot API | HTTP 429 Too Many Requests | Тест подтвердил классификацию `rate_limited` |
| Telegram Bot API | HTTP 500 | Тест подтвердил классификацию `telegram_server_error` |
| Telegram Bot API | Контекстный timeout | Тест подтвердил классификацию `request_timeout` |
| Источник Библии (GetBible) | HTTP 503 | Клиент возвращает безопасную ошибку со статусом, без тела ответа upstream |
| Источник Библии (GetBible) | Timeout | Клиент сообщает о превышении времени ожидания |
| PostgreSQL | `pg_sleep(30ms)` | Трассировка показывает задержку SQL, фактический disconnect DB не выполнялся |

В 7.2 отдельно проверены повторные updates и идемпотентность, повторная доставка callback, быстрая навигация, редактирование ответа и ошибки временного сохранения. Это функциональные регрессионные тесты, не отдельный тест нагрузки с массовым replay.

## 6. Что не покрыто

- **Полный end-to-end пользовательский путь:** нагрузка не проходит от Telegram update через полный `Bot.handleUpdate`, сервисы, PostgreSQL, Redis и отправку сообщений в одной транзакции измерения. Слои проверены отдельно.
- **CPU/RAM backend-контейнера:** CI не поднимает production backend как контейнер под эту нагрузку; `docker stats` для приложения не собирался. Использовать ресурсы GitHub runner как замену ресурсам production-контейнера было бы некорректно.
- **Реальный сбой PostgreSQL:** проверена искусственная серверная SQL-задержка через `pg_sleep`, но не остановка контейнера, сетевой разрыв или Toxiproxy.
- **Telegram и внешние Bible API:** использовались локальные fake HTTP-серверы. Реальные внешние endpoint не нагружались.
- **Измерение cold cache:** PostgreSQL-навигация использует созданные опубликованные тестовые вопросы; отдельного сравнения холодного/тёплого кэша в этих профилях нет.
- **Длинная soak-нагрузка:** тесты предназначены для воспроизводимой короткой проверки на 1/5/10 учеников, а не многочасового теста стабильности.

Для CPU/RAM, реального сетевого fault injection и сквозного сценария нужен отдельный локальный или staging прогон всего Compose-стека с изолированной БД, fake Telegram endpoint и Toxiproxy. Эти показатели не подменяются приблизительными значениями.

## 7. Повторный запуск

CI автоматически запускает PostgreSQL профиль с 1000 переходами на ученика и Telegram profiles. Для локального запуска на отдельной тестовой базе после применения миграций:

```bash
cd backend
RUN_LOAD_PROFILE=1 LOAD_PROFILE_ITERATIONS=1000 TEST_DATABASE_URL="postgres://..." \
  go test ./internal/repository -run TestQuestionNavigationLoadProfiles -count=1 -v

go test ./internal/telegrambot -run \
  'TestUpdateWorkerPoolLoadProfiles|TestTelegramAPILoadProfiles|TestTelegramHTTPClientHandlesRateLimitServerFailureAndTimeout' \
  -count=1 -v

go test ./internal/bibletext -run TestGetBibleClientHandlesUnavailableAndSlowSource -count=1 -v
```

`TEST_DATABASE_URL` должен указывать на отдельную тестовую БД с применёнными миграциями. Нагрузочный тест создаёт фикстуры в этой базе; не запускайте его против production или базы с данными учеников.

## 8. Изменённые файлы

- `backend/internal/repository/navigation_load_test.go`
- `backend/internal/telegrambot/load_profile_test.go`
- `backend/internal/bibletext/source_failure_test.go`
- `.github/workflows/ci.yml`

Production handlers, конфигурация приложения и production Compose в рамках 7.3 не менялись. Все изменения находятся в `feature/stage-7-final-audit`; `main` не изменён.
