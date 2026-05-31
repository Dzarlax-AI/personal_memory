package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Server
	Port   string
	APIKey string

	OAuth OAuthConfig

	// Qdrant
	QdrantURL string

	// Embeddings
	EmbedURL string

	// Memory
	MemoryUser       string
	CacheTTL         time.Duration
	DedupThreshold   float64
	ContradictionLow float64

	// Backup
	BackupInterval time.Duration
	KeepSnapshots  int

	// Todoist
	EnableTodoist bool
	TodoistToken  string

	// Viz
	EnableViz              bool
	VizSimilarityThreshold float64

	// Domain (for Traefik labels in docker-compose)
	MemoryDomain string

	// RAG
	EnableRAG            bool
	RAGDocumentsDir      string
	RAGChunkMaxBytes     int
	RAGFolderTopK        int
	RAGFolderThreshold   float64
	RAGCollectionChunks  string
	RAGCollectionFolders string
	RAGReindexInterval   time.Duration // 0 disables the in-server auto-rescan
}

type OAuthConfig struct {
	Enabled               bool
	Issuer                string
	Resource              string
	Audience              string
	JWKSURL               string
	AuthorizationServers  []string
	Scopes                []string
	ResourceDocumentation string
}

func Load() *Config {
	cfg := &Config{
		Port:   envOrDefault("MCP_PORT", "8000"),
		APIKey: os.Getenv("API_KEY"),

		QdrantURL: envOrDefault("QDRANT_URL", "http://memory-qdrant:6333"),
		EmbedURL:  envOrDefault("EMBED_URL", "http://memory-embeddings:80"),

		MemoryUser:       envOrDefault("MEMORY_USER", "claude"),
		CacheTTL:         envDuration("CACHE_TTL", 60*time.Second),
		DedupThreshold:   envFloat("DEDUP_THRESHOLD", 0.97),
		ContradictionLow: envFloat("CONTRADICTION_LOW", 0.60),

		BackupInterval: envDuration("BACKUP_INTERVAL_HOURS", 24*time.Hour),
		KeepSnapshots:  envInt("KEEP_SNAPSHOTS", 7),

		EnableTodoist: envBool("ENABLE_TODOIST"),
		TodoistToken:  os.Getenv("TODOIST_TOKEN"),

		EnableViz:              envBool("ENABLE_VIZ"),
		VizSimilarityThreshold: envFloat("VIZ_SIMILARITY_THRESHOLD", 0.65),

		MemoryDomain: os.Getenv("MEMORY_DOMAIN"),

		EnableRAG:            envBool("ENABLE_RAG"),
		RAGDocumentsDir:      envOrDefault("RAG_DOCUMENTS_DIR", "/root/documents/personal"),
		RAGChunkMaxBytes:     envInt("RAG_CHUNK_MAX_BYTES", 1500),
		RAGFolderTopK:        envInt("RAG_FOLDER_TOP_K", 3),
		RAGFolderThreshold:   envFloat("RAG_FOLDER_THRESHOLD", 0.50),
		RAGCollectionChunks:  envOrDefault("RAG_COLLECTION_CHUNKS", "doc_chunks"),
		RAGCollectionFolders: envOrDefault("RAG_COLLECTION_FOLDERS", "doc_folders"),
		RAGReindexInterval:   envDuration("RAG_REINDEX_INTERVAL_MINUTES", 0),
	}
	cfg.OAuth = loadOAuthConfig(cfg.MemoryDomain)
	return cfg
}

func loadOAuthConfig(memoryDomain string) OAuthConfig {
	resource := os.Getenv("OAUTH_RESOURCE")
	if resource == "" && memoryDomain != "" {
		resource = "https://mcp." + memoryDomain
	}

	issuer := os.Getenv("OAUTH_ISSUER")
	authServers := envCSV("OAUTH_AUTHORIZATION_SERVERS")
	if len(authServers) == 0 && issuer != "" {
		authServers = []string{issuer}
	}

	scopes := envCSV("OAUTH_SCOPES")
	if len(scopes) == 0 {
		scopes = []string{"memory:mcp"}
	}

	audience := os.Getenv("OAUTH_AUDIENCE")
	if audience == "" {
		audience = resource
	}

	return OAuthConfig{
		Enabled:               envBool("OAUTH_ENABLED"),
		Issuer:                issuer,
		Resource:              resource,
		Audience:              audience,
		JWKSURL:               os.Getenv("OAUTH_JWKS_URL"),
		AuthorizationServers:  authServers,
		Scopes:                scopes,
		ResourceDocumentation: os.Getenv("OAUTH_RESOURCE_DOCUMENTATION"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	return os.Getenv(key) == "true"
}

func envCSV(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if key == "BACKUP_INTERVAL_HOURS" {
		h, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return time.Duration(h) * time.Hour
	}
	if key == "RAG_REINDEX_INTERVAL_MINUTES" {
		m, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return time.Duration(m) * time.Minute
	}
	s, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return time.Duration(s) * time.Second
}
