package chunker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/amantur/docs-helper/internal/models"
)

const (
	defaultChunkSize = 500
	defaultOverlap   = 50
)

// Chunker splits parsed documents into overlapping text chunks.
type Chunker interface {
	Chunk(ctx context.Context, doc *models.ParsedDocument) ([]models.Chunk, error)
}

type chunker struct {
	log       *slog.Logger
	chunkSize int
	overlap   int
}

// New returns a Chunker with the given configuration. Zero or negative values
// for chunkSize or overlap fall back to defaults (500 and 50 respectively).
func New(log *slog.Logger, chunkSize, overlap int) Chunker {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if overlap <= 0 {
		overlap = defaultOverlap
	}
	return &chunker{
		log:       log,
		chunkSize: chunkSize,
		overlap:   overlap,
	}
}

// sentence holds a single sentence with its provenance.
type sentence struct {
	text    string
	pageNum int
	section string
}

// Chunk splits doc.Pages into overlapping chunks.
func (c *chunker) Chunk(ctx context.Context, doc *models.ParsedDocument) ([]models.Chunk, error) {
	if doc == nil {
		return nil, fmt.Errorf("chunker: parsed document is nil")
	}
	if len(doc.Pages) == 0 {
		return nil, nil
	}

	// Flatten pages into sentences, tracking page number and current section.
	sentences := c.extractSentences(doc.Pages)

	if len(sentences) == 0 {
		return nil, nil
	}

	// Build chunks with overlapping windows.
	chunks := c.buildChunks(doc.DocID, doc.UserID, sentences)

	c.log.DebugContext(ctx, "chunked document",
		"doc_id", doc.DocID,
		"pages", len(doc.Pages),
		"chunks", len(chunks),
	)

	return chunks, nil
}

// extractSentences splits page text into sentences with provenance.
func (c *chunker) extractSentences(pages []models.Page) []sentence {
	var out []sentence

	for _, page := range pages {
		currentSection := ""
		sectionIdx := 0

		for _, raw := range splitSentences(page.Text) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}

			// Advance section if the current sentence matches a section header.
			if sectionIdx < len(page.Sections) && raw == page.Sections[sectionIdx] {
				currentSection = raw
				sectionIdx++
				continue
			}

			out = append(out, sentence{
				text:    raw,
				pageNum: page.Number,
				section: currentSection,
			})
		}
	}

	return out
}

// buildChunks assembles sentences into overlapping word-counted chunks.
func (c *chunker) buildChunks(docID, userID int64, sentences []sentence) []models.Chunk {
	var (
		chunks     []models.Chunk
		chunkIndex int
		prevWords  []string // last overlap words from previous chunk
		buf        []string // accumulated word slice for current chunk
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		chunk := c.finalizeChunk(docID, userID, chunkIndex, buf, &prevWords)
		chunks = append(chunks, chunk)
		chunkIndex++
		buf = append([]string(nil), prevWords...)
	}

	for _, s := range sentences {
		words := strings.Fields(s.text)
		for _, w := range words {
			if len(buf) >= c.chunkSize {
				flush()
			}
			buf = append(buf, w)
		}
	}

	flush()

	// Set provenance on each chunk from the sentence that contributed its first word.
	c.annotateChunks(chunks, sentences)

	return chunks
}

// finalizeChunk creates a Chunk from accumulated words, captures overlap, and
// updates prevWords for the next chunk.
func (c *chunker) finalizeChunk(docID, userID int64, index int, words []string, prevWords *[]string) models.Chunk {
	text := strings.Join(words, " ")

	if c.overlap > 0 && len(words) > c.overlap {
		*prevWords = words[len(words)-c.overlap:]
	} else {
		*prevWords = words
	}

	return models.Chunk{
		ID:         fmt.Sprintf("%d-%d", docID, index),
		DocID:      docID,
		UserID:     userID,
		Text:       text,
		ChunkIndex: index,
	}
}

// annotateChunks walks sentences and assigns the section/page of the first
// sentence that falls into each chunk.
func (c *chunker) annotateChunks(chunks []models.Chunk, sentences []sentence) {
	if len(chunks) == 0 {
		return
	}

	sIdx := 0
	for i := range chunks {
		chunkStart := strings.Fields(chunks[i].Text)[0]
		// Advance sIdx to the sentence containing chunkStart.
		for sIdx < len(sentences) {
			sWords := strings.Fields(sentences[sIdx].text)
			for _, w := range sWords {
				if w == chunkStart {
					chunks[i].PageNum = sentences[sIdx].pageNum
					chunks[i].Section = sentences[sIdx].section
					goto nextChunk
				}
			}
			sIdx++
		}
	nextChunk:
	}
}

// splitSentences splits text on sentence boundaries: '.', '!', '?', and '\n'.
// Each delimiter is emitted as its own sentence to preserve section-header
// detection.
func splitSentences(text string) []string {
	var parts []string
	start := 0

	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch r {
		case '.', '!', '?':
			if i > start {
				parts = append(parts, text[start:i])
			}
			parts = append(parts, string(r))
			i += size
			start = i
		case '\n':
			if i > start {
				parts = append(parts, text[start:i])
			}
			i += size
			start = i
		default:
			i += size
		}
	}

	if start < len(text) {
		parts = append(parts, text[start:])
	}

	return parts
}
