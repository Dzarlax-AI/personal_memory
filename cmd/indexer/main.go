package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddingidentity"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/Dzarlax-AI/personal-memory/internal/rag"
)

func main() {
	cfg, err := config.LoadIndexer()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	qcChunks := qdrant.NewClient(cfg.QdrantURL, cfg.RAGCollectionChunks)
	qcFolders := qdrant.NewClient(cfg.QdrantURL, cfg.RAGCollectionFolders)
	ec := embeddings.NewClient(cfg.EmbedURL)

	if _, err := embeddingidentity.Ensure(ctx, ec, []*qdrant.Client{qcChunks, qcFolders}, embeddingidentity.Expected{
		ModelID:       cfg.EmbedModelID,
		ModelRevision: cfg.EmbedModelRevision,
	}, cfg.AdoptExistingEmbeddingIdentity); err != nil {
		slog.Error("embedding identity verification failed", "error", err)
		os.Exit(1)
	}

	if err := rag.EnsureIndexes(ctx, qcChunks, qcFolders); err != nil {
		slog.Error("failed to init RAG indexes", "error", err)
		os.Exit(1)
	}

	indexer := rag.NewIndexer(qcChunks, qcFolders, ec, cfg.RAGDocumentsDir, cfg.RAGChunkMaxBytes)
	if err := indexer.Run(ctx); err != nil {
		slog.Error("indexer failed", "error", err)
		os.Exit(1)
	}
}
