# Диагностика производительности Telegram-бота

Инструментирование включено в `backend/internal/performance` и подключено к PostgreSQL через `pgx.QueryTracer`. Метрики собираются отдельно для каждого Telegram update и выводятся одной агрегированной JSON-записью после обработки.

## Формат логов

### Telegram long polling

Запись с `operation=telegram.get_updates` содержит:
- `duration_ms` — время полного вызова `getUpdates`, включая штатное ожидание long polling;
- `updates_count` — сколько updates вернул Telegram;
- `long_poll_timeout_seconds` — заданный тайм-аут ожидания (30 секунд);
- `result` — `ok` или `error`.

Это время получения обновлений, а не задержка реакции на нажатие. Длительный `getUpdates` при пустом ответе является ожидаемым поведением long polling.

### Полная обработка update

Одна запись с `operation=telegram.update.process` содержит:
- `update_id` — correlation ID update от Telegram;
- `duration_ms` — полное время обработки update внутри приложения;
- `result` — `ok` (update обработан) или `retry` (обработчик просит повторную доставку);
- `sql_query_count`, `sql_duration_ms` и `sql_by_command` — количество, суммарное время и группировка SQL по типу команды;
- `stages` — длительность каждого измеряемого этапа: число вызовов, успешных вызовов, ошибок, суммарное и максимальное время, а также счётчик исходов `results` (например, `hit`/`miss` у кэша глав);
- `postgres_pool` — снимок состояния пула: активные, ожидающие, свободные и максимальные соединения, а также накопленные счётчики ожидания пула.

В `stages` используются, в частности, следующие операции:

| Операция | Что измеряет |
|---|---|
| `telegram.answer_callback_query` | Подтверждение нажатия callback |
| `postgres.pool.acquire` | Ожидание свободного соединения PostgreSQL |
| `student.ensure` | Проверка/регистрация ученика |
| `learning.progress.check` | Получение и проверка текущего прогресса |
| `learning.current_question` | Восстановление текущего вопроса для текстового ответа |
| `learning.question.fetch` | Загрузка данных вопроса |
| `learning.question.transition` | Переход к предыдущему/следующему вопросу |
| `learning.answer.save` | Сохранение текстового или кнопочного ответа |
| `learning.course.open` / `learning.lesson.open` | Открытие курса и запуск урока |
| `bible.chapter_links` | Подготовка ссылок на полные главы |
| `bible.chapter.fetch` | Получение текста главы из сервиса глав |
| `bible.chapter.postgres_cache.read` / `bible.chapter.postgres_cache.write` | Чтение и запись кэша глав PostgreSQL; чтение отдельно различает cache hit и cache miss |
| `bible.provider.midvash` / `bible.provider.getbible` | Запросы к внешним источникам текста Библии |
| `telegraph.link_cache` / `telegraph.create_page` | Чтение/создание ссылки и публикация страницы Telegraph |
| `telegram.send_message` / `telegram.edit_message_text` | Отправка и редактирование Telegram-сообщений |

SQL-трассировщик сохраняет только тип SQL-команды и длительность. Текст запросов, аргументы, содержимое сообщений, callback query ID и токен Telegram не попадают в performance summary.

## Сценарии для замеров

После выкладки ветки выполнить каждый сценарий несколько десятков раз на тестовом аккаунте, сохраняя JSON-логи:

1. Открыть каталог курсов: `catalog.courses.list`.
2. Выбрать курс: `catalog.course.get` и `catalog.course.lessons.list`.
3. Открыть урок: `catalog.lesson.detail`, `learning.course.open`, `learning.lesson.open` и `learning.question.fetch`.
4. Перейти к следующему и предыдущему вопросу: `learning.progress.check` и `learning.question.transition`.
5. Сохранить ответ кнопкой: `learning.progress.check`, `learning.question.fetch`, `learning.answer.save` и `learning.question.refresh`.
6. Сохранить текстовый ответ: `learning.current_question`, `learning.answer.save` и `learning.question.refresh`.
7. Открыть вопрос с библейскими ссылками: `bible.chapter_links` и вложенные операции источника/Telegraph. Сравнить cache hit и cache miss.

Для сравнения сценариев группировать записи `telegram.update.process` по `stages` и смотреть не только среднее время, но и `max_ms` и ошибки. Для набора повторений дополнительно рассчитывать p50/p95/p99 по `duration_ms` каждого update. Сравнивать `sql_query_count` и `sql_duration_ms` между повторениями одного сценария.

Пример выборки из файла с JSON-логами:

```bash
jq 'select(.operation == "telegram.update.process") |
  {update_id, duration_ms, result, sql_query_count, sql_duration_ms, sql_by_command, stages}' app.jsonl
```

Не сравнивать длительность пустого `getUpdates` с длительностью callback. При оценке задержки нажатия смотреть на `telegram.answer_callback_query` и полное `telegram.update.process`.

## Ограничения первого этапа

Инструментирование само по себе не публикует измеренные значения и не меняет поведение бота. Реальный рейтинг самых медленных операций и фактическое число SQL на сценарий появятся после запуска этой ветки с подключёнными Telegram, PostgreSQL и Telegraph. До получения таких логов любые числовые оценки были бы предположением.
