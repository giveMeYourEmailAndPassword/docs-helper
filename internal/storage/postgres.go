package storage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amantur/docs-helper/internal/models"
)

type postgres struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newPostgres(log *slog.Logger, databaseURL string) (*postgres, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	log.Info("storage: connected to PostgreSQL")
	return &postgres{pool: pool, log: log}, nil
}

// GetOrCreateUser inserts a user or updates the username on conflict (telegram_id is unique).
func (p *postgres) GetOrCreateUser(ctx context.Context, telegramID int64, username string) (*models.User, error) {
	const query = `
		INSERT INTO users (telegram_id, username)
		VALUES ($1, $2)
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id, telegram_id, username, created_at
	`

	var u models.User
	err := p.pool.QueryRow(ctx, query, telegramID, username).Scan(&u.ID, &u.TelegramID, &u.Username, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get or create user: %w", err)
	}
	return &u, nil
}

// CreateDocument inserts a document row and returns the full record.
func (p *postgres) CreateDocument(ctx context.Context, userID int64, originalName, storagePath, fileType string) (*models.Document, error) {
	const query = `
		INSERT INTO documents (user_id, original_name, storage_path, file_type)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, original_name, storage_path, file_type, status, pages_count, error_msg, created_at
	`

	var d models.Document
	err := p.pool.QueryRow(ctx, query, userID, originalName, storagePath, fileType).Scan(
		&d.ID, &d.UserID, &d.OriginalName, &d.StoragePath, &d.FileType,
		&d.Status, &d.PagesCount, &d.ErrorMsg, &d.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	return &d, nil
}

// UpdateDocumentStatus sets the status and optional error message for a document.
func (p *postgres) UpdateDocumentStatus(ctx context.Context, docID int64, status models.DocumentStatus, errorMsg string) error {
	const query = `UPDATE documents SET status = $1, error_msg = $2 WHERE id = $3`

	tag, err := p.pool.Exec(ctx, query, status, errorMsg, docID)
	if err != nil {
		return fmt.Errorf("update document status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update document status: document %d not found", docID)
	}
	return nil
}

// UpdatePagesCount sets the page count for a document.
func (p *postgres) UpdatePagesCount(ctx context.Context, docID int64, count int) error {
	const query = `UPDATE documents SET pages_count = $1 WHERE id = $2`

	tag, err := p.pool.Exec(ctx, query, count, docID)
	if err != nil {
		return fmt.Errorf("update pages count: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update pages count: document %d not found", docID)
	}
	return nil
}

// GetDocument retrieves a single document by ID.
func (p *postgres) GetDocument(ctx context.Context, docID int64) (*models.Document, error) {
	const query = `
		SELECT id, user_id, original_name, storage_path, file_type, status, pages_count, error_msg, created_at
		FROM documents
		WHERE id = $1
	`

	var d models.Document
	err := p.pool.QueryRow(ctx, query, docID).Scan(
		&d.ID, &d.UserID, &d.OriginalName, &d.StoragePath, &d.FileType,
		&d.Status, &d.PagesCount, &d.ErrorMsg, &d.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	return &d, nil
}

// GetUserDocuments returns all documents belonging to a user, newest first.
func (p *postgres) GetUserDocuments(ctx context.Context, userID int64) ([]models.Document, error) {
	const query = `
		SELECT id, user_id, original_name, storage_path, file_type, status, pages_count, error_msg, created_at
		FROM documents
		WHERE user_id = $1
		ORDER BY created_at DESC
	`

	rows, err := p.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("get user documents: %w", err)
	}
	defer rows.Close()

	var docs []models.Document
	for rows.Next() {
		var d models.Document
		if err := rows.Scan(
			&d.ID, &d.UserID, &d.OriginalName, &d.StoragePath, &d.FileType,
			&d.Status, &d.PagesCount, &d.ErrorMsg, &d.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("get user documents: scan: %w", err)
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get user documents: iterate: %w", err)
	}

	if docs == nil {
		docs = []models.Document{}
	}
	return docs, nil
}

// DeleteDocument removes a document row. Vector cleanup is handled separately.
func (p *postgres) DeleteDocument(ctx context.Context, docID int64) error {
	const query = `DELETE FROM documents WHERE id = $1`

	tag, err := p.pool.Exec(ctx, query, docID)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete document: document %d not found", docID)
	}
	return nil
}

// Close shuts down the connection pool.
func (p *postgres) Close() {
	p.pool.Close()
}
