package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/amantur/docs-helper/internal/chunker"
	"github.com/amantur/docs-helper/internal/config"
	"github.com/amantur/docs-helper/internal/embedder"
	"github.com/amantur/docs-helper/internal/logger"
	"github.com/amantur/docs-helper/internal/models"
	"github.com/amantur/docs-helper/internal/parser"
	"github.com/amantur/docs-helper/internal/pipeline"
	"github.com/amantur/docs-helper/internal/storage"
	"github.com/amantur/docs-helper/internal/vectordb"
)

func main() {
	log := logger.New(os.Getenv("LOG_LEVEL"))

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Storage
	st, err := storage.New(log, cfg.DatabaseURL)
	if err != nil {
		log.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// Core services
	p := parser.New(log)
	c := chunker.New(log, 500, 50)
	e := embedder.New(log, cfg.OpenAIBaseURL, cfg.OpenAIKey, cfg.EmbeddingModel)
	v := vectordb.New(log, cfg.QdrantURL)

	// Pipeline
	pl := pipeline.New(log, st, p, c, e, v)

	// HTTP server
	mux := http.NewServeMux()
	h := &handler{log: log, storage: st, pipeline: pl, cfg: cfg, vectordb: v}

	mux.HandleFunc("POST /api/v1/documents", h.uploadDocument)
	mux.HandleFunc("GET /api/v1/documents/{id}", h.getDocument)
	mux.HandleFunc("POST /api/v1/search", h.search)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", h.deleteDocument)
	mux.HandleFunc("GET /api/v1/documents", h.listDocuments)
	mux.HandleFunc("GET /health", h.health)

	srv := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      withLogging(log, mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Info("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	log.Info("docs-helper starting", "port", cfg.HTTPPort)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}

type handler struct {
	log      *slog.Logger
	storage  storage.Storage
	pipeline *pipeline.Pipeline
	cfg      *config.Config
	vectordb vectordb.VectorDB
}

// POST /api/v1/documents
func (h *handler) uploadDocument(w http.ResponseWriter, r *http.Request) {
	user, err := h.resolveUser(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid telegram_id")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer file.Close()

	// Save locally under /data volume
	baseDir := "/data/documents"
	userDir := fmt.Sprintf("%s/%d", baseDir, user.ID)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		h.log.Error("create user dir", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store file")
		return
	}

	safeName := sanitizeFilename(header.Filename)
	storagePath := fmt.Sprintf("%s/%s", userDir, safeName)

	dst, err := os.Create(storagePath)
	if err != nil {
		h.log.Error("create file", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		h.log.Error("write file", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store file")
		return
	}

	// Detect file type
	fileType := detectFileType(header.Filename)

	doc, err := h.storage.CreateDocument(r.Context(), user.ID, header.Filename, storagePath, fileType)
	if err != nil {
		h.log.Error("create document", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create document")
		return
	}

	// Run pipeline asynchronously
	go func() {
		ctx := context.Background()
		if err := h.pipeline.Process(ctx, doc.ID, storagePath); err != nil {
			h.log.Error("pipeline failed", "doc_id", doc.ID, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, doc)
}

// GET /api/v1/documents/{id}
func (h *handler) getDocument(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid document id")
		return
	}

	doc, err := h.storage.GetDocument(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

// GET /api/v1/documents?telegram_id=...
func (h *handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	user, err := h.resolveUser(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid telegram_id")
		return
	}

	docs, err := h.storage.GetUserDocuments(r.Context(), user.ID)
	if err != nil {
		h.log.Error("list documents", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list documents")
		return
	}

	writeJSON(w, http.StatusOK, docs)
}

// POST /api/v1/search
func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TelegramID int64  `json:"telegram_id"`
		Username   string `json:"username"`
		Query      string `json:"query"`
		Limit      int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.Limit <= 0 || req.Limit > 20 {
		req.Limit = 10
	}

	user, err := h.storage.GetOrCreateUser(r.Context(), req.TelegramID, req.Username)
	if err != nil {
		h.log.Error("get or create user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve user")
		return
	}

	results, err := h.pipeline.Search(r.Context(), user.ID, req.Query, req.Limit)
	if err != nil {
		h.log.Error("search failed", "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	// Group results by document for the RAG bot's contradiction detection
	resp := struct {
		Results   []models.SearchResult `json:"results"`
		GroupedBy map[string][]models.SearchResult `json:"grouped_by_doc"`
	}{
		Results:   results,
		GroupedBy: groupByDoc(results),
	}

	writeJSON(w, http.StatusOK, resp)
}

// DELETE /api/v1/documents/{id}?telegram_id=...
func (h *handler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid document id")
		return
	}

	user, err := h.resolveUser(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid telegram_id")
		return
	}

	doc, err := h.storage.GetDocument(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	if doc.UserID != user.ID {
		writeError(w, http.StatusForbidden, "not your document")
		return
	}

	// Delete vectors from Qdrant first
	_ = h.vectordb.DeleteByDocument(r.Context(), "docs_chunks", id)

	if err := h.storage.DeleteDocument(r.Context(), id); err != nil {
		h.log.Error("delete document", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete document")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// GET /health
func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- helpers ---

func (h *handler) resolveUser(r *http.Request) (*models.User, error) {
	telegramID, err := strconv.ParseInt(r.URL.Query().Get("telegram_id"), 10, 64)
	if err != nil || telegramID == 0 {
		return nil, fmt.Errorf("invalid telegram_id")
	}
	username := r.URL.Query().Get("username")
	return h.storage.GetOrCreateUser(r.Context(), telegramID, username)
}

func parsePathID(r *http.Request, name string) (int64, error) {
	v := r.PathValue(name)
	return strconv.ParseInt(v, 10, 64)
}

func detectFileType(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "pdf"
	case strings.HasSuffix(lower, ".docx"):
		return "docx"
	default:
		return "unknown"
	}
}

// sanitizeFilename removes path separators and keeps only safe characters.
func sanitizeFilename(name string) string {
	base := filepath.Base(name)           // strip any directory components
	ext := filepath.Ext(base)
	body := strings.TrimSuffix(base, ext)

	// Replace unsafe chars with underscore
	body = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, body)

	body = strings.Trim(body, "._-")
	if body == "" {
		body = "document"
	}

	// Keep extension: lowercase, alphanumeric plus dot
	ext = strings.ToLower(ext)
	ext = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			return r
		}
		return '_'
	}, ext)

	return body + ext
}

func groupByDoc(results []models.SearchResult) map[string][]models.SearchResult {
	groups := make(map[string][]models.SearchResult)
	for _, r := range results {
		name := r.DocName
		if name == "" {
			name = fmt.Sprintf("doc-%d", r.Chunk.DocID)
		}
		groups[name] = append(groups[name], r)
	}
	return groups
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

