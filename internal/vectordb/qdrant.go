package vectordb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/amantur/docs-helper/internal/models"
)

// VectorDB is the interface for vector storage and semantic search.
type VectorDB interface {
	EnsureCollection(ctx context.Context, collectionName string, vectorSize int) error
	Upsert(ctx context.Context, collectionName string, chunks []models.Chunk) error
	Search(ctx context.Context, collectionName string, queryVector []float32, userID int64, limit int) ([]models.SearchResult, error)
	DeleteByDocument(ctx context.Context, collectionName string, docID int64) error
}

// QdrantClient implements VectorDB against the Qdrant REST API.
type QdrantClient struct {
	log     *slog.Logger
	baseURL string
	client  *http.Client
}

// New creates a new Qdrant REST client.
func New(log *slog.Logger, qdrantURL string) VectorDB {
	return &QdrantClient{
		log:     log,
		baseURL: strings.TrimRight(qdrantURL, "/"),
		client:  &http.Client{},
	}
}

// --- request / response types ---

type collectionRequest struct {
	Vectors collectionVectors `json:"vectors"`
}

type collectionVectors struct {
	Size     int    `json:"size"`
	Distance string `json:"distance"`
}

type upsertRequest struct {
	Points []point `json:"points"`
}

type point struct {
	ID      uint64       `json:"id"`
	Vector  []float32    `json:"vector"`
	Payload pointPayload `json:"payload"`
}

type pointPayload struct {
	DocID      int64  `json:"doc_id"`
	UserID     int64  `json:"user_id"`
	Text       string `json:"text"`
	PageNum    int    `json:"page_num"`
	Section    string `json:"section"`
	ChunkIndex int    `json:"chunk_index"`
}

type searchRequest struct {
	Vector      []float32    `json:"vector"`
	Filter      searchFilter `json:"filter"`
	Limit       int          `json:"limit"`
	WithPayload bool         `json:"with_payload"`
}

type searchFilter struct {
	Must []filterCondition `json:"must"`
}

type filterCondition struct {
	Key   string     `json:"key"`
	Match matchValue `json:"match"`
}

type matchValue struct {
	Value any `json:"value"`
}

type deleteRequest struct {
	Filter searchFilter `json:"filter"`
}

type searchResponse struct {
	Result []scoredPoint `json:"result"`
}

type scoredPoint struct {
	ID      uint64       `json:"id"`
	Version int          `json:"version"`
	Score   float32      `json:"score"`
	Payload pointPayload `json:"payload"`
}

// EnsureCollection creates the collection if it doesn't exist. Idempotent.
func (c *QdrantClient) EnsureCollection(ctx context.Context, collectionName string, vectorSize int) error {
	req := collectionRequest{
		Vectors: collectionVectors{
			Size:     vectorSize,
			Distance: "Cosine",
		},
	}

	resp, err := c.do(ctx, http.MethodPut, "/collections/"+collectionName, req)
	if err != nil {
		return fmt.Errorf("ensure collection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ensure collection: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Upsert inserts or updates chunk points in the collection.
func (c *QdrantClient) Upsert(ctx context.Context, collectionName string, chunks []models.Chunk) error {
	points := make([]point, len(chunks))
	for i, ch := range chunks {
		id, err := strconv.ParseUint(ch.ID, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid chunk id %q: %w", ch.ID, err)
		}
		points[i] = point{
			ID:     id,
			Vector: ch.Embedding,
			Payload: pointPayload{
				DocID:      ch.DocID,
				UserID:     ch.UserID,
				Text:       ch.Text,
				PageNum:    ch.PageNum,
				Section:    ch.Section,
				ChunkIndex: ch.ChunkIndex,
			},
		}
	}

	resp, err := c.do(ctx, http.MethodPut, "/collections/"+collectionName+"/points", upsertRequest{Points: points})
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Search performs a vector similarity search filtered by user_id.
func (c *QdrantClient) Search(ctx context.Context, collectionName string, queryVector []float32, userID int64, limit int) ([]models.SearchResult, error) {
	body := searchRequest{
		Vector: queryVector,
		Filter: searchFilter{
			Must: []filterCondition{
				{Key: "user_id", Match: matchValue{Value: userID}},
			},
		},
		Limit:       limit,
		WithPayload: true,
	}

	resp, err := c.do(ctx, http.MethodPost, "/collections/"+collectionName+"/points/search", body)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("search: decode response: %w", err)
	}

	results := make([]models.SearchResult, len(sr.Result))
	for i, sp := range sr.Result {
		results[i] = models.SearchResult{
			Chunk: models.Chunk{
				ID:         fmt.Sprintf("%d", sp.ID),
				DocID:      sp.Payload.DocID,
				UserID:     sp.Payload.UserID,
				Text:       sp.Payload.Text,
				PageNum:    sp.Payload.PageNum,
				Section:    sp.Payload.Section,
				ChunkIndex: sp.Payload.ChunkIndex,
			},
			Score: sp.Score,
		}
	}

	return results, nil
}

// DeleteByDocument removes all points belonging to a document.
func (c *QdrantClient) DeleteByDocument(ctx context.Context, collectionName string, docID int64) error {
	body := deleteRequest{
		Filter: searchFilter{
			Must: []filterCondition{
				{Key: "doc_id", Match: matchValue{Value: docID}},
			},
		},
	}

	resp, err := c.do(ctx, http.MethodPost, "/collections/"+collectionName+"/points/delete?wait=true", body)
	if err != nil {
		return fmt.Errorf("delete by document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete by document: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// do sends an HTTP request with a JSON body and returns the response.
func (c *QdrantClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return c.client.Do(req)
}
