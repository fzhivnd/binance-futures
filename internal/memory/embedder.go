package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	pgvector "github.com/pgvector/pgvector-go"
)

type EmbedderConfig struct {
	APIKey  string
	Model   string // "text-embedding-3-small"
	Timeout time.Duration
}

type Embedder struct {
	client openai.Client
	cfg    EmbedderConfig
}

func NewEmbedder(cfg EmbedderConfig) *Embedder {
	client := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithRequestTimeout(cfg.Timeout),
	)
	return &Embedder{client: client, cfg: cfg}
}

// Embed converts feature text into a pgvector using the OpenAI embeddings API.
func (e *Embedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	start := time.Now()
	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.cfg.Model),
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(text),
		},
	})
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("embed_call failed", "latency_ms", elapsed.Milliseconds(), "model", e.cfg.Model, "error", err)
		return pgvector.Vector{}, fmt.Errorf("embedding API call failed: %w", err)
	}

	if len(resp.Data) == 0 {
		return pgvector.Vector{}, fmt.Errorf("empty embedding response")
	}

	slog.Debug("embed_call", "latency_ms", elapsed.Milliseconds(), "model", e.cfg.Model)
	vec := make([]float32, len(resp.Data[0].Embedding))
	for i, v := range resp.Data[0].Embedding {
		vec[i] = float32(v)
	}

	return pgvector.NewVector(vec), nil
}
