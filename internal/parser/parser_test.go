package parser

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestParse_UnsupportedFormat(t *testing.T) {
	p := New(slog.New(slog.DiscardHandler))
	_, err := p.Parse(context.Background(), strings.NewReader("hello"), "file.txt")
	if err == nil {
		t.Fatal("expected error for .txt, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected 'unsupported' in error, got: %v", err)
	}
}

func TestParse_EmptyDocument(t *testing.T) {
	p := New(slog.New(slog.DiscardHandler))
	_, err := p.Parse(context.Background(), strings.NewReader(""), "file.pdf")
	if err == nil {
		t.Fatal("expected error for empty, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}

func TestParse_InvalidPDF(t *testing.T) {
	p := New(slog.New(slog.DiscardHandler))
	_, err := p.Parse(context.Background(), strings.NewReader("not a pdf"), "file.pdf")
	if err == nil {
		t.Fatal("expected error for invalid PDF, got nil")
	}
}
