# Библейская обучающая платформа

Фундамент проекта для будущей платформы с Telegram-ботом, обучающими уроками, проверкой ответов через ИИ, внутренними диалогами учеников с кураторами и веб-панелью управления. На первом этапе реализованы HTTP backend, подключение к PostgreSQL, health endpoint и минимальная страница проверки соединения. Бизнес-таблиц и логики обучения пока нет.

## Технологии

- Go 1.27 и стандартный `net/http`;
- `github.com/jackc/pgx/v5` и пул `pgxpool` для PostgreSQL;
- React + TypeScript + Vite;
- PostgreSQL 18;
- Docker Compose и Nginx.

## Требования

Для обычного запуска нужны Docker Engine и Docker Compose v2 (или Docker Desktop для Windows). Для запуска проверок без Docker дополнительно нужны Go 1.27+ и Node.js 24 LTS с npm.

## Подготовка окружения

Создайте `.env` из примера. Файл `.env` уже добавлен в `.gitignore` и не должен попадать в Git.

Linux / macOS:

```bash
cp .env.example .env
```

Windows PowerShell:

```powershell
Copy-Item .env.example .env
```

Откройте `.env` и измените `POSTGRES_PASSWORD`. Compose передаст это значение PostgreSQL и backend, поэтому отдельная переменная пароля базы в `.env` не нужна. Пароль в примере — только заполнитель для локальной разработки. Не публикуйте `.env` и не вставляйте реальные токены или пароли в исходники.

`TELEGRAM_BOT_TOKEN` добавлен в пример для будущего этапа. Сейчас приложение его не читает: Telegram-бот ещё не реализован. После копирования `.env.example` в `.env` можно вставить свой токен именно в локальный `.env`; Git его игнорирует.

## Запуск через Docker Compose

Выполните в корне репозитория одну команду:

```bash
docker compose up --build -d
```

После запуска:

- frontend: <http://localhost:8081>;
- backend: <http://localhost:8080>;
- PostgreSQL доступен с хоста на `127.0.0.1:5432`.

Порты можно изменить через `FRONTEND_PORT`, `BACKEND_PORT` и `POSTGRES_PORT` в `.env`. Публикуемые порты привязаны к localhost, а не к сетевым интерфейсам компьютера. Между контейнерами backend подключается к PostgreSQL по внутреннему имени `postgres`, а frontend направляет `/health/` и `/api/` через Nginx в backend.

## Health endpoint

- `GET /health/live` — возвращает HTTP 200 и `{"status":"ok"}`, если HTTP-процесс отвечает.
- `GET /health/ready` — возвращает HTTP 200 и `{"status":"ok"}`, если PostgreSQL доступен; при проблеме возвращает HTTP 503 и `{"status":"unavailable"}`. Подробности ошибки не раскрываются клиенту.

На frontend проверка запускается автоматически и доступна по кнопке «Проверить соединение». В production-контейнере запрос идёт через Nginx, поэтому отдельная настройка CORS не требуется. Для локального Vite dev-сервера proxy по умолчанию направлен на `http://localhost:8080`; его можно изменить переменной `VITE_DEV_BACKEND_TARGET`.

## Логи и остановка

Показать логи всех сервисов:

```bash
docker compose logs -f
```

Логи только backend:

```bash
docker compose logs -f backend
```

Остановить контейнеры, сохранив данные базы:

```bash
docker compose down
```

Удалить контейнеры **вместе с данными PostgreSQL**:

```bash
docker compose down -v
```

База хранится в именованном томе `postgres_data`, который переживает обычный `docker compose down`. Команда с `-v` удаляет этот том без возможности восстановить данные, если предварительно не создана резервная копия. Для PostgreSQL 18 volume смонтирован в `/var/lib/postgresql`, как требует официальный образ.

## Проверки

Backend (из каталога `backend`):

```bash
gofmt -w ./cmd ./internal
go test ./...
```

Frontend (из каталога `frontend`):

```bash
npm install
npm run typecheck
npm run build
```

Проверить итоговую конфигурацию Compose:

```bash
docker compose config --quiet
```

Проверить, что собранные сервисы доступны:

```bash
docker compose ps
docker compose logs --tail=100 backend
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

Windows PowerShell вместо `curl` можно использовать `Invoke-RestMethod`:

```powershell
Invoke-RestMethod http://localhost:8080/health/live
Invoke-RestMethod http://localhost:8080/health/ready
```

## Структура

```text
.
├── backend/
│   ├── .dockerignore
│   ├── cmd/api/main.go
│   ├── internal/config/
│   ├── internal/database/
│   ├── internal/httpserver/
│   ├── Dockerfile
│   └── go.mod
├── frontend/
│   ├── .dockerignore
│   ├── src/
│   ├── Dockerfile
│   ├── nginx.conf
│   ├── package.json
│   └── vite.config.ts
├── docker-compose.yml
├── .env.example
├── .gitignore
└── README.md
```

## Ограничения первого этапа

- Проект рассчитан только на локальную разработку. Полноценная аутентификация и авторизация отсутствуют; не публикуйте сервисы в интернете.
- Telegram-бот, проверка ответов через ИИ, курсы, уроки, бизнес-таблицы, внутренние диалоги, импорт DOCX/ZIP и веб-панель управления не реализованы.
- База пока содержит только системные объекты PostgreSQL, без таблиц приложения.
- Переменная `TELEGRAM_BOT_TOKEN` пока является примером для будущего этапа и не используется кодом.
