package config

import (
	"fmt"
	"os"
)

type Config struct {
	DatabaseURL    string
	QdrantURL      string
	S3Endpoint     string
	S3AccessKey    string
	S3SecretKey    string
	S3Bucket       string
	OpenAIKey      string
	OpenAIBaseURL  string
	EmbeddingModel string
	HTTPPort       string
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:    envOrDefault("DATABASE_URL", "postgres://docs:docs@localhost:5432/docs_helper?sslmode=disable"),
		QdrantURL:      envOrDefault("QDRANT_URL", "http://localhost:6333"),
		S3Endpoint:     envOrDefault("S3_ENDPOINT", "localhost:9000"),
		S3AccessKey:    envOrDefault("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:    envOrDefault("S3_SECRET_KEY", "minioadmin"),
		S3Bucket:       envOrDefault("S3_BUCKET", "docs"),
		OpenAIKey:      os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:  envOrDefault("OPENAI_BASE_URL", "http://localhost:11434/v1"),
		EmbeddingModel: envOrDefault("EMBEDDING_MODEL", "nomic-embed-text"),
		HTTPPort:       envOrDefault("HTTP_PORT", "8080"),
	}

	if cfg.OpenAIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}

	return cfg, nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
