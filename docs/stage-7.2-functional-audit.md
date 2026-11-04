# Этап 7.2. Функциональный аудит Telegram-бота

Дата: 2026-11-04  
Репозиторий: `Quantum103/Telegramm-Bot`  
Ветка: `feature/stage-7-final-audit`

## 1. Результат

Проверены имеющиеся автоматизированные проверки всех 14 пользовательских сценариев этапа 7.2. Недостающие тесты для двух важных переходов усилены:

1. Изменение сохранённого single-choice ответа теперь проходит полный UI callback-путь: сохранение первого варианта, вход в режим редактирования, выбор второго варианта, создание новой попытки и проверка, что число отвеченных вопросов не увеличивается.
2. Завершение урока больше не подменяет счётчик отвеченных вопросов вручную. Тест отправляет реальные тестовые сообщения с ответами на оба вопроса, проверяет запрет преждевременного завершения и завершает урок только после сохранения обоих ответов.

Производственные обработчики в рамках 7.2 не менялись: аудит не выявил подтверждённой функциональной ошибки, требующей их изменения.

## 2. Матрица покрытия 14 сценариев

| № | Сценарий | Основные автоматизированные проверки |
|---:|---|---|
| 1 | Открытие каталога курсов | `TestStartRegistersStudentAndShowsCourses`, `TestEmptyCatalogIsExplained` |
| 2 | Переход к списку уроков | `TestSelectingCourseShowsOnlyThatCoursesLessons` |
| 3 | Открытие курса | `TestCourseModeChoiceStartsAICourseAndFirstLesson`, `TestCuratorModeSelectionConfirmsAndOpensFirstLesson` |
| 4 | Открытие урока | `TestOpeningLessonShowsFirstQuestionAndNavigation`, `TestLessonWithoutQuestionsShowsFriendlyMessage` |
| 5 | Переход «Далее» | `TestNavigationMovesForwardAndBackwardInSingleMessage`, `TestEnrollmentRepositoryAtomicQuestionNavigation` |
| 6 | Переход «Назад» | `TestNavigationMovesForwardAndBackwardInSingleMessage`, `TestSavedTextAnswerRestoredWhenNavigatingBack` |
| 7 | Выбор одного варианта | `TestSingleChoiceOptionIsSaved`, `TestOptionBelongingToAnotherQuestionIsRejected` |
| 8 | Выбор нескольких вариантов | `TestMultipleChoiceNeedsExplicitConfirmation`, `TestMultiChoiceDesiredStateIsIdempotentAndRestoresDraftFromMarkup`, `TestMultiChoiceCallbackRedeliveryDoesNotToggleOptionOff` |
| 9 | Изменение сохранённого ответа | `TestChangingTextAnswerCreatesAttemptAndPreservesProgressCount`, новый `TestChangingSavedSingleChoiceAnswerCreatesNewAttempt`, DB-проверка изменения выбранного варианта в `TestCatalogChoiceSubmissionProgressAndCompletion` |
| 10 | Отправка текстового ответа | `TestAnswerTextSavedDeletesIncomingAndDoesNotEvaluate`, `TestTextAnswerTemporaryPersistenceFailureDoesNotAcknowledgeUpdate` |
| 11 | Завершение урока | Усиленный `TestLessonCanBeCompletedOnlyAfterAllAnswersAreSaved`, `TestCatalogChoiceSubmissionProgressAndCompletion` |
| 12 | Возврат в каталог | `TestCuratorChatReturnsToCoursesAndLessonDeliveryFollowsCurrentScreen`, `TestSecondCourseIsBlockedButCatalogRemainsAvailable` |
| 13 | Вопрос с библейскими ссылками | `TestOpeningLessonRendersFormattedPassageAndInlineChapterLink`, `TestBiblePassageReferenceFormatsInclusiveRange`, проверки диапазонов и переводов в `bible_reference_test.go` |
| 14 | Доставка ответа куратора | `TestDeliveryWorkerUsesTelegramAPIAndMarksSuccessfulDelivery`, `TestDeliveryWorkerIdentifiesCuratorAsMessageSender`, `TestDeliveryWorkerSendsCuratorNotificationWithConversationButton`, `TestDeliveryWorkerDefersCuratorReplyUntilStudentLeavesLessonScreen` |

Дополнительные регрессионные проверки:

- повторная отправка одного Telegram update: `TestSameTelegramUpdateIsIdempotent`;
- повтор callback режима курса: `TestRedeliveredModeCallbackReusesTheSameCourseRun`;
- восстановление курса и текущего вопроса после перезапуска: `TestStartRestoresSavedCourseAndQuestionAfterBotRestart`;
- временная ошибка сохранения ответа не подтверждает update как обработанный;
- ошибки Telegram API не должны приводить к потере или ложному подтверждению бизнес-операции;
- устаревшие callback не должны перемещать ученика через вопросы.

## 3. Усиленные тесты

### Изменение single-choice ответа

Файл: `backend/internal/telegrambot/answers_test.go`

Новый тест `TestChangingSavedSingleChoiceAnswerCreatesNewAttempt` проверяет:

- первый вариант сохраняется;
- на экране сохранённого ответа доступна кнопка «Изменить ответ»;
- режим редактирования снова показывает варианты;
- повторный выбор другого варианта обновляет сохранённый ответ;
- создаётся новая попытка;
- количество отвеченных вопросов остаётся равным одному;
- экран показывает уже новый вариант.

### Завершение урока через реальные тестовые ответы

Файл: `backend/internal/telegrambot/bot_test.go`

Тест `TestLessonCanBeCompletedOnlyAfterAllAnswersAreSaved` теперь:

1. Открывает урок через Telegram callback.
2. Проверяет, что кнопки завершения пока нет.
3. Отправляет поддельный callback завершения и проверяет, что урок не завершается.
4. Сохраняет ответ на первый вопрос.
5. Переходит ко второму вопросу.
6. Сохраняет второй ответ.
7. Проверяет, что прогресс содержит два сохранённых ответа.
8. Завершает урок.
9. Проверяет статус `completed` и кнопки дальнейшей навигации.

Таким образом, тест больше не присваивает `AnsweredQuestions` напрямую.

## 4. Что именно проверяют тесты

- **Telegram UI и callback flow:** fake Telegram API и тестовые service/repository реализации позволяют проверить содержимое сообщений, inline-кнопки, навигацию, повторы и обработку ошибок.
- **PostgreSQL:** integration tests используют реальную PostgreSQL из CI. В частности, проверяют каталог, порядок уроков, сохранение/изменение ответов, попытки и завершение урока.
- **Telegram delivery:** worker тестируется через локальный `httptest.Server`, который принимает HTTP запросы и возвращает управляемые ответы Bot API.
- **Библейские ссылки:** форматирование и построение ссылок проверяются на тестовых данных и fake publisher.

## 5. Ограничения

- Реальные сообщения в Telegram от имени production-бота не отправлялись.
- Реальные Telegram-аккаунты ученика и куратора в ручном сценарии не использовались.
- Доставка куратора проверяется на локальном тестовом HTTP-сервере, а не через внешний Telegram Bot API.
- Открытие полной главы проверяется через тестовый publisher; доступность внешнего источника Библии и Telegraph при сетевом сбое относится к отказным тестам этапа 7.3.
- Автоматизированные UI-тесты используют управляемые тестовые сервисы. Они проверяют контракт взаимодействия, но не заменяют отдельный ручной smoke-test с реальным Telegram-клиентом.

## 6. Итог

Все 14 пользовательских сценариев имеют автоматизированное покрытие в существующих тестах. Два наиболее важных сценария — изменение сохранённого выбора и завершение урока после фактического сохранения ответов — дополнительно усилены.

Утверждение о прохождении тестов относится только к тем GitHub Actions run, которые завершились со статусом `success`. Реальные Telegram API и production-аккаунты не тестировались.

Полный отчёт 7.1: `docs/stage-7.1-performance-audit.md`.
