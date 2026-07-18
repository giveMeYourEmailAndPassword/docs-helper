// Package embedder provides an OpenAI-compatible embeddings API client.
package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBatchSize = 20
	requestTimeout   = 60 * time.Second
	maxRetries       = 3
)

var backoffDelays = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

// Embedder generates embeddings for input texts.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type adapterType int

const (
	adapterOpenAI adapterType = iota
	adapterOllama
)

type embedder struct {
	log      *slog.Logger
	baseURL  string
	apiKey   string
	model    string
	adapter  adapterType
	client   *http.Client
}

// New creates an Embedder backed by an OpenAI-compatible API.
func New(log *slog.Logger, baseURL, apiKey, model string) Embedder {
	adapter := adapterOpenAI
	if baseURL == "" || strings.Contains(baseURL, "ollama") || strings.Contains(baseURL, "11434") {
		adapter = adapterOllama
	}
	return &embedder{
		log:     log,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		adapter: adapter,
		client:  &http.Client{Timeout: requestTimeout},
	}
}

func (e *embedder) buildURL() string {
	if e.adapter == adapterOllama {
		// Strip /v1 suffix added by compose config, use native API
		base := strings.TrimSuffix(e.baseURL, "/v1")
		return base + "/api/embed"
	}
	return e.baseURL + "/embeddings"
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

type embeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns float32 embeddings for each input text, preserving order.
func (e *embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	var all [][]float32
	for i := 0; i < len(texts); i += defaultBatchSize {
		end := i + defaultBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		embeddings, err := e.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embedder: batch %d-%d: %w", i, end, err)
		}
		all = append(all, embeddings...)
	}

	return all, nil
}

func (e *embedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embeddingRequest{
		Model:          e.model,
		Input:          texts,
		EncodingFormat: "float",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := e.buildURL()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := backoffDelays[attempt-1]
			e.log.Warn("embedder: retrying request",
				"attempt", attempt,
				"delay", delay,
				"error", lastErr,
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := e.doRequest(ctx, url, bodyBytes)
		if err != nil {
			lastErr = err
			continue
		}

		// Retry on 429 or 5xx.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error: status %d", resp.StatusCode)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
		}

		return e.parseResponse(resp)
	}

	return nil, fmt.Errorf("embedder: max retries exceeded: %w", lastErr)
}

func (e *embedder) doRequest(ctx context.Context, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	return e.client.Do(req)
}

func (e *embedder) parseResponse(resp *http.Response) ([][]float32, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if e.adapter == adapterOllama {
		var embResp ollamaEmbedResponse
		if err := json.Unmarshal(body, &embResp); err != nil {
			return nil, fmt.Errorf("unmarshal ollama response: %w", err)
		}
		return embResp.Embeddings, nil
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	embeddings := make([][]float32, len(embResp.Data))
	for i, d := range embResp.Data {
		embeddings[i] = d.Embedding
	}

	return embeddings, nil
}
