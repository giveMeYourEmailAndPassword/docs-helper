# AGENTS.md — Docs Helper

## Проект

Docs Helper — Go-сервис обработки документов для Telegram RAG-бота. Принимает PDF/DOCX, извлекает текст с сохранением структуры (страницы, разделы), разбивает на чанки, создаёт embeddings и сохраняет в Qdrant. Мультитенантная архитектура: каждый пользователь работает только со своими документами.

## Стек

- Go 1.26, `net/http` (стандартный роутер), `log/slog`
- PostgreSQL — пользователи, документы, статусы
- Qdrant — векторный поиск по чанкам
- S3/MinIO — хранение оригинальных файлов
- OpenAI API (совместимый) — embeddings (`text-embedding-3-small`)
- PDF: `github.com/ledongthuc/pdf` (текстовый слой)
- DOCX: `github.com/nguyenthenguyen/docx`
- Docker Compose — локальная разработка

## Архитектура

```
Telegram Bot (krugo-bot или новый)
   ↓ HTTP REST
docs-helper (Go)
   ├── POST /api/v1/documents     — загрузка файла
   ├── GET  /api/v1/documents/:id  — статус + метаданные
   ├── POST /api/v1/search         — поиск по чанкам
   └── DELETE /api/v1/documents/:id — удаление документа и векторов
        ↓
   ┌────┼────┬──────────┐
   ↓    ↓    ↓          ↓
  S3   PDF/DOCX  OpenAI   Qdrant
       парсер    Embed    (вектора)
                 API
```

## Ключевые файлы

| Файл | Назначение |
|---|---|
| `cmd/helper/main.go` | Точка входа: HTTP-сервер, роутинг |
| `internal/config/config.go` | Конфигурация из env-переменных |
| `internal/parser/parser.go` | Интерфейс парсера документов |
| `internal/parser/pdf.go` | Извлечение текста из PDF (страницы, позиции) |
| `internal/parser/docx.go` | Извлечение текста из DOCX (разделы, параграфы) |
| `internal/chunker/chunker.go` | Разбивка текста на чанки с метаданными |
| `internal/embedder/embedder.go` | OpenAI-совместимый API для embeddings |
| `internal/vectordb/qdrant.go` | Qdrant: создание коллекций, upsert, search |
| `internal/storage/postgres.go` | PostgreSQL: users, documents, статусы |
| `internal/pipeline/pipeline.go` | Оркестрация: parse → chunk → embed → store |
| `migrations/001_create_tables.sql` | Схема БД |

## Пайплайн обработки

```
received → extracting → chunking → embedding → ready
                                      ↘ error (на любом этапе)
```

1. **received** — документ сохранён в S3, запись в БД создана
2. **extracting** — парсинг PDF/DOCX, извлечение текста и структуры
3. **chunking** — разбивка на overlapping-чанки (~500 токенов, overlap 50)
4. **embedding** — запрос к embedding API, получение векторов
5. **ready** — чанки и векторы сохранены в Qdrant, документ доступен для поиска

## Мультитенантность

- `documents.user_id` — владелец документа
- Каждый вектор в Qdrant содержит `user_id` в payload
- При поиске обязательный фильтр `user_id = ?`
- При удалении пользователя каскадно чистим документы, файлы, векторы

## Env-переменные

| Переменная | Назначение | По умолчанию |
|---|---|---|
| `DATABASE_URL` | PostgreSQL DSN | `postgres://docs:docs@localhost:5432/docs_helper?sslmode=disable` |
| `QDRANT_URL` | Qdrant gRPC/REST | `http://localhost:6334` |
| `S3_ENDPOINT` | MinIO/S3 endpoint | `localhost:9000` |
| `S3_ACCESS_KEY` | S3 access key | `minioadmin` |
| `S3_SECRET_KEY` | S3 secret key | `minioadmin` |
| `S3_BUCKET` | S3 bucket name | `docs` |
| `OPENAI_API_KEY` | OpenAI API key | — |
| `OPENAI_BASE_URL` | OpenAI base URL | `https://api.openai.com/v1` |
| `EMBEDDING_MODEL` | Модель для embeddings | `text-embedding-3-small` |
| `HTTP_PORT` | Порт HTTP-сервера | `8080` |

## Для AI-агентов

При масштабных изменениях (новые пакеты, смена архитектуры, зависимости) — обнови этот файл.
