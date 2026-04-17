package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server exposes RAG as MCP tools registered on the shared memory MCP server.
type Server struct {
	chunks  *qdrant.Client
	folders *qdrant.Client
	embed   *embeddings.Client
	cfg     *config.Config
	indexer *Indexer
}

func NewServer(chunks, folders *qdrant.Client, embed *embeddings.Client, cfg *config.Config) *Server {
	idx := NewIndexer(chunks, folders, embed, cfg.RAGDocumentsDir, cfg.RAGChunkMaxBytes)
	return &Server{
		chunks:  chunks,
		folders: folders,
		embed:   embed,
		cfg:     cfg,
		indexer: idx,
	}
}

func (s *Server) InitCollections(ctx context.Context) error {
	// Embed a test string to get the vector size.
	vec, err := s.embed.Embed(ctx, "init")
	if err != nil {
		return fmt.Errorf("embed init: %w", err)
	}
	size := len(vec)
	if err := s.chunks.EnsureCollection(ctx, size); err != nil {
		return fmt.Errorf("ensure chunks collection: %w", err)
	}
	if err := s.folders.EnsureCollection(ctx, size); err != nil {
		return fmt.Errorf("ensure folders collection: %w", err)
	}
	return nil
}

func (s *Server) RegisterTools(mcpSrv *server.MCPServer) {
	mcpSrv.AddTool(mcp.NewTool("search_documents",
		mcp.WithDescription("Search personal documents using semantic similarity. Uses hierarchical search: finds relevant folders first, then searches chunks within those folders. Falls back to flat search if no folder exceeds the threshold."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
		mcp.WithNumber("limit", mcp.Description("Max results to return (default 5)")),
		mcp.WithString("mode", mcp.Description("Search mode: 'hierarchical' (default) or 'flat'")),
	), s.handleSearchDocuments)

	mcpSrv.AddTool(mcp.NewTool("reindex_documents",
		mcp.WithDescription("Trigger a re-index of the personal documents directory. Skips unchanged files (hash check). Run this after adding or editing documents."),
	), s.handleReindexDocuments)
}

func (s *Server) handleSearchDocuments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}

	limit := 5
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	mode := "hierarchical"
	if m, ok := args["mode"].(string); ok && m != "" {
		mode = m
	}

	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embed error: %v", err)), nil
	}

	var points []qdrant.Point
	if mode == "flat" {
		points, err = s.flatSearch(ctx, vec, limit)
	} else {
		points, err = s.hierarchicalSearch(ctx, vec, limit)
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
	}

	results := make([]map[string]interface{}, 0, len(points))
	for _, p := range points {
		results = append(results, map[string]interface{}{
			"score":       p.Score,
			"text":        p.Payload["text"],
			"file_path":   p.Payload["file_path"],
			"heading":     p.Payload["heading"],
			"chunk_index": p.Payload["chunk_index"],
		})
	}

	b, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(b)), nil
}

func (s *Server) hierarchicalSearch(ctx context.Context, vec []float32, limit int) ([]qdrant.Point, error) {
	threshold := s.cfg.RAGFolderThreshold
	folderPoints, err := s.folders.Search(ctx, vec, s.cfg.RAGFolderTopK, nil, &threshold)
	if err != nil {
		return nil, err
	}

	if len(folderPoints) == 0 {
		return s.flatSearch(ctx, vec, limit)
	}

	// Collect matched folder paths.
	var folderPaths []interface{}
	for _, fp := range folderPoints {
		if p, ok := fp.Payload["folder_path"].(string); ok {
			folderPaths = append(folderPaths, p)
		}
	}

	if len(folderPaths) == 0 {
		return s.flatSearch(ctx, vec, limit)
	}

	// Search chunks filtered to those folders.
	filter := map[string]interface{}{
		"should": func() []map[string]interface{} {
			conds := make([]map[string]interface{}, len(folderPaths))
			for i, fp := range folderPaths {
				conds[i] = map[string]interface{}{
					"key": "folder_path",
					"match": map[string]interface{}{"value": fp},
				}
			}
			return conds
		}(),
	}

	points, err := s.chunks.Search(ctx, vec, limit, filter, nil)
	if err != nil {
		return nil, err
	}

	// Fall back to flat if no results in filtered folders.
	if len(points) == 0 {
		return s.flatSearch(ctx, vec, limit)
	}
	return points, nil
}

func (s *Server) flatSearch(ctx context.Context, vec []float32, limit int) ([]qdrant.Point, error) {
	return s.chunks.Search(ctx, vec, limit, nil, nil)
}

func (s *Server) handleReindexDocuments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := s.indexer.Run(ctx); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("reindex error: %v", err)), nil
	}
	var sb strings.Builder
	sb.WriteString("Reindex complete. Documents directory: ")
	sb.WriteString(s.cfg.RAGDocumentsDir)
	return mcp.NewToolResultText(sb.String()), nil
}
