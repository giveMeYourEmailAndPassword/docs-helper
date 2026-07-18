package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/amantur/docs-helper/internal/chunker"
	"github.com/amantur/docs-helper/internal/embedder"
	"github.com/amantur/docs-helper/internal/models"
	"github.com/amantur/docs-helper/internal/parser"
	"github.com/amantur/docs-helper/internal/storage"
	"github.com/amantur/docs-helper/internal/vectordb"
)

const collectionName = "docs_chunks"

// Pipeline orchestrates the full document processing flow.
type Pipeline struct {
	log      *slog.Logger
	storage  storage.Storage
	parser   parser.Parser
	chunker  chunker.Chunker
	embedder embedder.Embedder
	vectordb vectordb.VectorDB
}

func New(
	log *slog.Logger,
	st storage.Storage,
	p parser.Parser,
	c chunker.Chunker,
	e embedder.Embedder,
	v vectordb.VectorDB,
) *Pipeline {
	return &Pipeline{
		log:      log,
		storage:  st,
		parser:   p,
		chunker:  c,
		embedder: e,
		vectordb: v,
	}
}

// Process runs the full pipeline for a document identified by docID.
// The document must already exist in storage with status "received".
func (pl *Pipeline) Process(ctx context.Context, docID int64, filePath string) error {
	doc, err := pl.storage.GetDocument(ctx, docID)
	if err != nil {
		return fmt.Errorf("get document: %w", err)
	}

	if err := pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusExtracting, ""); err != nil {
		return fmt.Errorf("update status extracting: %w", err)
	}

	// Stage 1: Parse
	f, err := os.Open(filePath)
	if err != nil {
		_ = pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusError, err.Error())
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	parsed, err := pl.parser.Parse(ctx, f, filePath)
	parsed.DocID = docID
	parsed.UserID = doc.UserID

	pl.log.Info("document parsed", "doc_id", docID, "pages", len(parsed.Pages))

	// Update pages count
	if err := pl.storage.UpdatePagesCount(ctx, docID, len(parsed.Pages)); err != nil {
		return fmt.Errorf("update pages count: %w", err)
	}

	if err := pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusChunking, ""); err != nil {
		return fmt.Errorf("update status chunking: %w", err)
	}

	// Stage 2: Chunk
	chunks, err := pl.chunker.Chunk(ctx, parsed)
	if err != nil {
		_ = pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusError, err.Error())
		return fmt.Errorf("chunk: %w", err)
	}

	pl.log.Info("document chunked", "doc_id", docID, "chunks", len(chunks))

	if err := pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusEmbedding, ""); err != nil {
		return fmt.Errorf("update status embedding: %w", err)
	}

	// Stage 3: Embed
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	vectors, err := pl.embedder.Embed(ctx, texts)
	if err != nil {
		_ = pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusError, err.Error())
		return fmt.Errorf("embed: %w", err)
	}

	for i := range chunks {
		chunks[i].Embedding = vectors[i]
	}

	pl.log.Info("embeddings generated", "doc_id", docID, "vectors", len(vectors))

	// Stage 4: Store in Qdrant
	if len(chunks) > 0 {
		// Ensure collection exists (vector size from first embedding)
		if err := pl.vectordb.EnsureCollection(ctx, collectionName, len(chunks[0].Embedding)); err != nil {
			return fmt.Errorf("ensure collection: %w", err)
		}

		if err := pl.vectordb.Upsert(ctx, collectionName, chunks); err != nil {
			_ = pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusError, err.Error())
			return fmt.Errorf("upsert: %w", err)
		}
	}

	// Done
	if err := pl.storage.UpdateDocumentStatus(ctx, docID, models.StatusReady, ""); err != nil {
		return fmt.Errorf("update status ready: %w", err)
	}

	pl.log.Info("document ready", "doc_id", docID)

	return nil
}

// Search performs semantic search across a user's documents.
func (pl *Pipeline) Search(ctx context.Context, userID int64, query string, limit int) ([]models.SearchResult, error) {
	vectors, err := pl.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	if len(vectors) == 0 {
		return nil, fmt.Errorf("no embedding returned for query")
	}

	results, err := pl.vectordb.Search(ctx, collectionName, vectors[0], userID, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	// Enrich results with document names
	for i, r := range results {
		doc, err := pl.storage.GetDocument(ctx, r.Chunk.DocID)
		if err == nil {
			results[i].DocName = doc.OriginalName
		}
	}

	return results, nil
}
