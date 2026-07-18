package storage

import (
	"context"
	"log/slog"

	"github.com/amantur/docs-helper/internal/models"
)

// Storage is the PostgreSQL-backed persistence layer.
type Storage interface {
	GetOrCreateUser(ctx context.Context, telegramID int64, username string) (*models.User, error)
	CreateDocument(ctx context.Context, userID int64, originalName, storagePath, fileType string) (*models.Document, error)
	UpdateDocumentStatus(ctx context.Context, docID int64, status models.DocumentStatus, errorMsg string) error
	UpdatePagesCount(ctx context.Context, docID int64, count int) error
	GetDocument(ctx context.Context, docID int64) (*models.Document, error)
	GetUserDocuments(ctx context.Context, userID int64) ([]models.Document, error)
	DeleteDocument(ctx context.Context, docID int64) error
	Close()
}

// New opens a pgxpool and returns the concrete implementation.
func New(log *slog.Logger, databaseURL string) (Storage, error) {
	return newPostgres(log, databaseURL)
}
