package config

import (
	"context"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddingidentity"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

// PrepareEmbeddings constructs active clients only, reusing one backend for
// shared profiles, then validates every active collection before workers start.
func (c *Config) PrepareEmbeddings(ctx context.Context, memory, chunks, folders *qdrant.Client) (embeddings.Embedder, embeddings.Embedder, error) {
	if c.EmbeddingProfiles == nil {
		client := embeddings.NewClient(c.EmbedURL)
		var collections []*qdrant.Client
		for _, q := range []*qdrant.Client{memory, chunks, folders} {
			if q != nil {
				collections = append(collections, q)
			}
		}
		_, err := embeddingidentity.Ensure(ctx, client, collections, embeddingidentity.Expected{ModelID: c.EmbedModelID, ModelRevision: c.EmbedModelRevision, InputProfile: c.EmbedInputProfile}, c.AdoptExistingEmbeddingIdentity)
		return client, client, err
	}
	clients := map[string]*embeddings.Backend{}
	bindings := map[string]*embeddingidentity.Binding{}
	order := []string{}
	add := func(name string, collections ...*qdrant.Client) (*embeddings.Backend, error) {
		if clients[name] == nil {
			cfg := c.EmbeddingProfiles.Profiles[name]
			client, err := embeddings.NewBackend(cfg)
			if err != nil {
				return nil, err
			}
			clients[name] = client
			bindings[name] = &embeddingidentity.Binding{Backend: client, Config: cfg}
			order = append(order, name)
		}
		bindings[name].Collections = append(bindings[name].Collections, collections...)
		return clients[name], nil
	}
	var mem, rag embeddings.Embedder
	if memory != nil {
		client, err := add(c.EmbeddingProfiles.Bindings.Memory.Profile, memory)
		if err != nil {
			return nil, nil, err
		}
		mem = client
	}
	if chunks != nil {
		client, err := add(c.EmbeddingProfiles.Bindings.RAG.Profile, chunks, folders)
		if err != nil {
			return nil, nil, err
		}
		rag = client
	}
	active := make([]embeddingidentity.Binding, 0, len(order))
	for _, name := range order {
		active = append(active, *bindings[name])
	}
	if err := embeddingidentity.EnsureBindings(ctx, active, c.AdoptExistingEmbeddingIdentity); err != nil {
		return nil, nil, err
	}
	return mem, rag, nil
}
