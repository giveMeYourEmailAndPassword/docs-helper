package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	h := &handler{log: log, storage: st, pipeline: pl, cfg: cfg}

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
}

// POST /api/v1/documents
func (h *handler) uploadDocument(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUserID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
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

	// Save to S3/MinIO (placeholder: local storage for now)
	storagePath := fmt.Sprintf("documents/%d/%s", userID, header.Filename)
	// TODO: upload to S3; for now save locally
	_ = storagePath
	_ = file

	// Detect file type
	fileType := detectFileType(header.Filename)

	doc, err := h.storage.CreateDocument(r.Context(), userID, header.Filename, storagePath, fileType)
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

// GET /api/v1/documents?user_id=...
func (h *handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUserID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}

	docs, err := h.storage.GetUserDocuments(r.Context(), userID)
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
		UserID int64  `json:"user_id"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
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

	results, err := h.pipeline.Search(r.Context(), req.UserID, req.Query, req.Limit)
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

// DELETE /api/v1/documents/{id}
func (h *handler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid document id")
		return
	}

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

func parseUserID(r *http.Request) (int64, error) {
	v := r.URL.Query().Get("user_id")
	return strconv.ParseInt(v, 10, 64)
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

