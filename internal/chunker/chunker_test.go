package chunker

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/amantur/docs-helper/internal/models"
)

func TestChunker(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := New(log, 10, 3) // small windows for testing

	doc := &models.ParsedDocument{
		DocID:  42,
		UserID: 7,
		Pages: []models.Page{
			{
				Number:   1,
				Text:     "Hello world. This is a test. More text here!",
				Sections: []string{"Intro"},
			},
			{
				Number:   2,
				Text:     "Page two content. Extra sentence here. Final words.",
				Sections: nil,
			},
		},
	}

	chunks, err := c.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected non-zero chunks")
	}

	// Every chunk must carry provenance.
	for i, ch := range chunks {
		if ch.DocID != 42 {
			t.Errorf("chunk %d: DocID = %d, want 42", i, ch.DocID)
		}
		if ch.UserID != 7 {
			t.Errorf("chunk %d: UserID = %d, want 7", i, ch.UserID)
		}
		if ch.ChunkIndex != i {
			t.Errorf("chunk %d: ChunkIndex = %d, want %d", i, ch.ChunkIndex, i)
		}
		if ch.ID == "" {
			t.Errorf("chunk %d: empty ID", i)
		}
		if ch.Text == "" {
			t.Errorf("chunk %d: empty Text", i)
		}
		if ch.PageNum == 0 {
			t.Errorf("chunk %d: PageNum = 0", i)
		}
	}
}

func TestChunkerOverlap(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := New(log, 5, 2)

	doc := &models.ParsedDocument{
		DocID:  1,
		UserID: 1,
		Pages: []models.Page{
			{Number: 1, Text: "one two three four five six seven eight nine ten eleven twelve"},
		},
	}

	chunks, err := c.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Verify overlap: last 2 words of chunk 0 should be first 2 words of chunk 1.
	words0 := fields(chunks[0].Text)
	words1 := fields(chunks[1].Text)
	if len(words0) < 3 || len(words1) < 3 {
		t.Fatal("chunks too small")
	}
	if words0[len(words0)-2] != words1[0] || words0[len(words0)-1] != words1[1] {
		t.Errorf("overlap missing: chunk0 ends with %q %q, chunk1 starts with %q %q",
			words0[len(words0)-2], words0[len(words0)-1], words1[0], words1[1])
	}
}

func fields(s string) []string {
	var out []string
	inWord := false
	start := 0
	for i, r := range s {
		if r == ' ' {
			if inWord {
				out = append(out, s[start:i])
				inWord = false
			}
		} else {
			if !inWord {
				start = i
				inWord = true
			}
		}
	}
	if inWord {
		out = append(out, s[start:])
	}
	return out
}

func TestChunkerEmptyDoc(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := New(log, 10, 3)

	chunks, err := c.Chunk(context.Background(), &models.ParsedDocument{DocID: 1, UserID: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkerNilDoc(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := New(log, 10, 3)

	_, err := c.Chunk(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil doc")
	}
}
