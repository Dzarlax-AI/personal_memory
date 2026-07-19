package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Server
	Port              string
	APIKey            string
	AllowInsecureAuth bool

	OAuth OAuthConfig

	// Qdrant
	QdrantURL string

	// Embeddings
	EmbedURL string

	// Memory
	MemoryUser             string
	CacheTTL               time.Duration
	DedupThreshold         float64
	ContradictionLow       float64
	MutationMatchThreshold float64

	// Backup
	BackupInterval time.Duration
	KeepSnapshots  int

	// Todoist
	EnableTodoist bool
	TodoistToken  string

	// Viz
	EnableViz              bool
	VizProxySecret         string
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

func Load() (*Config, error) {
	cacheTTL, err := envDuration("CACHE_TTL", 60*time.Second)
	if err != nil {
		return nil, err
	}
	dedupThreshold, err := envFloat("DEDUP_THRESHOLD", 0.97)
	if err != nil {
		return nil, err
	}
	contradictionLow, err := envFloat("CONTRADICTION_LOW", 0.60)
	if err != nil {
		return nil, err
	}
	mutationThreshold, err := envFloat("MUTATION_MATCH_THRESHOLD", 0.90)
	if err != nil {
		return nil, err
	}
	backupInterval, err := envDuration("BACKUP_INTERVAL_HOURS", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	keepSnapshots, err := envInt("KEEP_SNAPSHOTS", 7)
	if err != nil {
		return nil, err
	}
	enableTodoist, err := envBool("ENABLE_TODOIST")
	if err != nil {
		return nil, err
	}
	allowInsecureAuth, err := envBool("ALLOW_INSECURE_AUTH")
	if err != nil {
		return nil, err
	}
	enableViz, err := envBool("ENABLE_VIZ")
	if err != nil {
		return nil, err
	}
	vizThreshold, err := envFloat("VIZ_SIMILARITY_THRESHOLD", 0.65)
	if err != nil {
		return nil, err
	}
	enableRAG, err := envBool("ENABLE_RAG")
	if err != nil {
		return nil, err
	}
	ragChunkMaxBytes, err := envInt("RAG_CHUNK_MAX_BYTES", 1500)
	if err != nil {
		return nil, err
	}
	ragFolderTopK, err := envInt("RAG_FOLDER_TOP_K", 3)
	if err != nil {
		return nil, err
	}
	ragFolderThreshold, err := envFloat("RAG_FOLDER_THRESHOLD", 0.50)
	if err != nil {
		return nil, err
	}
	ragReindexInterval, err := envDuration("RAG_REINDEX_INTERVAL_MINUTES", 0)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Port:              envOrDefault("MCP_PORT", "8000"),
		APIKey:            os.Getenv("API_KEY"),
		AllowInsecureAuth: allowInsecureAuth,

		QdrantURL: envOrDefault("QDRANT_URL", "http://memory-qdrant:6333"),
		EmbedURL:  envOrDefault("EMBED_URL", "http://memory-embeddings:80"),

		MemoryUser:             envOrDefault("MEMORY_USER", "claude"),
		CacheTTL:               cacheTTL,
		DedupThreshold:         dedupThreshold,
		ContradictionLow:       contradictionLow,
		MutationMatchThreshold: mutationThreshold,

		BackupInterval: backupInterval,
		KeepSnapshots:  keepSnapshots,

		EnableTodoist: enableTodoist,
		TodoistToken:  os.Getenv("TODOIST_TOKEN"),

		EnableViz:              enableViz,
		VizProxySecret:         os.Getenv("VIZ_PROXY_SECRET"),
		VizSimilarityThreshold: vizThreshold,

		MemoryDomain: os.Getenv("MEMORY_DOMAIN"),

		EnableRAG:            enableRAG,
		RAGDocumentsDir:      envOrDefault("RAG_DOCUMENTS_DIR", "/root/documents/personal"),
		RAGChunkMaxBytes:     ragChunkMaxBytes,
		RAGFolderTopK:        ragFolderTopK,
		RAGFolderThreshold:   ragFolderThreshold,
		RAGCollectionChunks:  envOrDefault("RAG_COLLECTION_CHUNKS", "doc_chunks"),
		RAGCollectionFolders: envOrDefault("RAG_COLLECTION_FOLDERS", "doc_folders"),
		RAGReindexInterval:   ragReindexInterval,
	}
	cfg.OAuth, err = loadOAuthConfig(cfg.MemoryDomain)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadIndexer loads only the configuration used by the standalone document
// indexer. Server authentication and optional HTTP features are deliberately
// outside this contract, so a deployment can run the indexer without setting
// API_KEY, OAuth, Todoist, or visualization variables.
func LoadIndexer() (*Config, error) {
	enableRAG, err := envBool("ENABLE_RAG")
	if err != nil {
		return nil, err
	}
	ragChunkMaxBytes, err := envInt("RAG_CHUNK_MAX_BYTES", 1500)
	if err != nil {
		return nil, err
	}
	ragFolderTopK, err := envInt("RAG_FOLDER_TOP_K", 3)
	if err != nil {
		return nil, err
	}
	ragFolderThreshold, err := envFloat("RAG_FOLDER_THRESHOLD", 0.50)
	if err != nil {
		return nil, err
	}
	ragReindexInterval, err := envDuration("RAG_REINDEX_INTERVAL_MINUTES", 0)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		QdrantURL:            envOrDefault("QDRANT_URL", "http://memory-qdrant:6333"),
		EmbedURL:             envOrDefault("EMBED_URL", "http://memory-embeddings:80"),
		EnableRAG:            enableRAG,
		RAGDocumentsDir:      envOrDefault("RAG_DOCUMENTS_DIR", "/root/documents/personal"),
		RAGChunkMaxBytes:     ragChunkMaxBytes,
		RAGFolderTopK:        ragFolderTopK,
		RAGFolderThreshold:   ragFolderThreshold,
		RAGCollectionChunks:  envOrDefault("RAG_COLLECTION_CHUNKS", "doc_chunks"),
		RAGCollectionFolders: envOrDefault("RAG_COLLECTION_FOLDERS", "doc_folders"),
		RAGReindexInterval:   ragReindexInterval,
	}
	if err := cfg.ValidateIndexer(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadOAuthConfig(memoryDomain string) (OAuthConfig, error) {
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

	enabled, err := envBool("OAUTH_ENABLED")
	if err != nil {
		return OAuthConfig{}, err
	}
	return OAuthConfig{
		Enabled:               enabled,
		Issuer:                issuer,
		Resource:              resource,
		Audience:              audience,
		JWKSURL:               os.Getenv("OAUTH_JWKS_URL"),
		AuthorizationServers:  authServers,
		Scopes:                scopes,
		ResourceDocumentation: os.Getenv("OAUTH_RESOURCE_DOCUMENTATION"),
	}, nil
}

func (c *Config) Validate() error {
	port, err := strconv.ParseUint(c.Port, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("MCP_PORT must be an integer between 1 and 65535")
	}
	if err := c.validateIndexerSettings(false); err != nil {
		return err
	}
	if c.CacheTTL <= 0 {
		return fmt.Errorf("CACHE_TTL must be greater than zero")
	}
	if err := validateThreshold("DEDUP_THRESHOLD", c.DedupThreshold); err != nil {
		return err
	}
	if err := validateThreshold("CONTRADICTION_LOW", c.ContradictionLow); err != nil {
		return err
	}
	if c.ContradictionLow >= c.DedupThreshold {
		return fmt.Errorf("CONTRADICTION_LOW must be less than DEDUP_THRESHOLD")
	}
	if err := validateThreshold("MUTATION_MATCH_THRESHOLD", c.MutationMatchThreshold); err != nil {
		return err
	}
	if err := validateThreshold("VIZ_SIMILARITY_THRESHOLD", c.VizSimilarityThreshold); err != nil {
		return err
	}
	if c.BackupInterval <= 0 {
		return fmt.Errorf("BACKUP_INTERVAL_HOURS must be greater than zero")
	}
	if c.KeepSnapshots < 1 {
		return fmt.Errorf("KEEP_SNAPSHOTS must be at least 1")
	}
	if c.APIKey == "" && !c.OAuth.Enabled && !c.AllowInsecureAuth {
		return fmt.Errorf("API_KEY is required when OAUTH_ENABLED=false (set ALLOW_INSECURE_AUTH=true only for isolated development)")
	}
	if c.EnableTodoist {
		if c.TodoistToken == "" {
			return fmt.Errorf("TODOIST_TOKEN is required when ENABLE_TODOIST=true")
		}
		if c.APIKey == "" {
			return fmt.Errorf("API_KEY is required when ENABLE_TODOIST=true because the Todoist endpoint is API-key-only")
		}
	}
	if c.EnableViz && c.VizProxySecret == "" && !c.AllowInsecureAuth {
		return fmt.Errorf("VIZ_PROXY_SECRET is required when ENABLE_VIZ=true")
	}
	if c.OAuth.Enabled {
		if err := validateHTTPURL("OAUTH_ISSUER", c.OAuth.Issuer); err != nil {
			return err
		}
		if err := validateHTTPURL("OAUTH_RESOURCE", c.OAuth.Resource); err != nil {
			return err
		}
		if strings.TrimSpace(c.OAuth.Audience) == "" {
			return fmt.Errorf("OAUTH_AUDIENCE is required when OAUTH_ENABLED=true")
		}
		if len(c.OAuth.AuthorizationServers) == 0 {
			return fmt.Errorf("OAUTH_AUTHORIZATION_SERVERS must contain at least one URL when OAUTH_ENABLED=true")
		}
		for _, server := range c.OAuth.AuthorizationServers {
			if err := validateHTTPURL("OAUTH_AUTHORIZATION_SERVERS", server); err != nil {
				return err
			}
		}
		if c.OAuth.JWKSURL != "" {
			if err := validateHTTPURL("OAUTH_JWKS_URL", c.OAuth.JWKSURL); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateIndexer validates the complete standalone indexer contract.
func (c *Config) ValidateIndexer() error {
	return c.validateIndexerSettings(true)
}

func (c *Config) validateIndexerSettings(requireEnabled bool) error {
	for name, raw := range map[string]string{"QDRANT_URL": c.QdrantURL, "EMBED_URL": c.EmbedURL} {
		if err := validateHTTPURL(name, raw); err != nil {
			return err
		}
	}
	if requireEnabled && !c.EnableRAG {
		return fmt.Errorf("ENABLE_RAG must be true for the standalone indexer")
	}
	if strings.TrimSpace(c.RAGDocumentsDir) == "" {
		return fmt.Errorf("RAG_DOCUMENTS_DIR cannot be empty")
	}
	if strings.TrimSpace(c.RAGCollectionChunks) == "" {
		return fmt.Errorf("RAG_COLLECTION_CHUNKS cannot be empty")
	}
	if strings.TrimSpace(c.RAGCollectionFolders) == "" {
		return fmt.Errorf("RAG_COLLECTION_FOLDERS cannot be empty")
	}
	if c.RAGChunkMaxBytes < 1 || c.RAGChunkMaxBytes > 1024*1024 {
		return fmt.Errorf("RAG_CHUNK_MAX_BYTES must be between 1 and 1048576")
	}
	if c.RAGFolderTopK < 1 {
		return fmt.Errorf("RAG_FOLDER_TOP_K must be at least 1")
	}
	if err := validateThreshold("RAG_FOLDER_THRESHOLD", c.RAGFolderThreshold); err != nil {
		return err
	}
	if c.RAGReindexInterval < 0 {
		return fmt.Errorf("RAG_REINDEX_INTERVAL_MINUTES cannot be negative")
	}
	return nil
}

func validateHTTPURL(name, raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be a valid HTTP(S) URL", name)
	}
	return nil
}

func validateThreshold(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 1 {
		return fmt.Errorf("%s must be greater than 0 and at most 1", name)
	}
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return b, nil
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

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return f, nil
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return i, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	if key == "BACKUP_INTERVAL_HOURS" {
		h, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}
		return time.Duration(h) * time.Hour, nil
	}
	if key == "RAG_REINDEX_INTERVAL_MINUTES" {
		m, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}
		return time.Duration(m) * time.Minute, nil
	}
	s, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return time.Duration(s) * time.Second, nil
}
