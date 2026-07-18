package models

import "time"

// DocumentStatus tracks pipeline progress.
type DocumentStatus string

const (
	StatusReceived   DocumentStatus = "received"
	StatusExtracting DocumentStatus = "extracting"
	StatusChunking   DocumentStatus = "chunking"
	StatusEmbedding  DocumentStatus = "embedding"
	StatusReady      DocumentStatus = "ready"
	StatusError      DocumentStatus = "error"
)

// User represents a Telegram user.
type User struct {
	ID         int64     `json:"id"`
	TelegramID int64     `json:"telegram_id"`
	Username   string    `json:"username"`
	CreatedAt  time.Time `json:"created_at"`
}

// Document is the DB record for an uploaded file.
type Document struct {
	ID           int64          `json:"id"`
	UserID       int64          `json:"user_id"`
	OriginalName string         `json:"original_name"`
	StoragePath  string         `json:"storage_path"`
	FileType     string         `json:"file_type"`
	Status       DocumentStatus `json:"status"`
	PagesCount   int            `json:"pages_count"`
	ErrorMsg     string         `json:"error_msg,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Page holds extracted text for a single page.
type Page struct {
	Number   int      `json:"number"`
	Text     string   `json:"text"`
	Sections []string `json:"sections,omitempty"`
}

// ParsedDocument is the output of the parser stage.
type ParsedDocument struct {
	DocID  int64  `json:"doc_id"`
	UserID int64  `json:"user_id"`
	Pages  []Page `json:"pages"`
}

// Chunk is a text fragment with full provenance metadata.
type Chunk struct {
	ID         string  `json:"id"`
	DocID      int64   `json:"doc_id"`
	UserID     int64   `json:"user_id"`
	Text       string  `json:"text"`
	Embedding  []float32 `json:"embedding,omitempty"`
	PageNum    int     `json:"page_num"`
	Section    string  `json:"section"`
	ChunkIndex int     `json:"chunk_index"`
}

// SearchResult is returned by vector search.
type SearchResult struct {
	Chunk    Chunk   `json:"chunk"`
	Score    float32 `json:"score"`
	DocName  string  `json:"doc_name"`
}
