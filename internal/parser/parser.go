package parser

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/amantur/docs-helper/internal/models"
)

// Parser extracts structured text from a document.
type Parser interface {
	Parse(ctx context.Context, reader io.Reader, filename string) (*models.ParsedDocument, error)
}

// New returns a Parser that auto-detects the format by file extension.
func New(log *slog.Logger) Parser {
	return &autoParser{log: log}
}

type autoParser struct {
	log *slog.Logger
}

func (p *autoParser) Parse(ctx context.Context, reader io.Reader, filename string) (*models.ParsedDocument, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading document: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty document")
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return parsePDF(p.log, data)
	case ".docx":
		return parseDOCX(p.log, data)
	default:
		return nil, fmt.Errorf("unsupported format: %q", ext)
	}
}
