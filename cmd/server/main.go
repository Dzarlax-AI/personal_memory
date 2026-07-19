package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/backup"
	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory"
	"github.com/Dzarlax-AI/personal-memory/internal/middleware"
	oauthauth "github.com/Dzarlax-AI/personal-memory/internal/oauth"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/Dzarlax-AI/personal-memory/internal/rag"
	"github.com/Dzarlax-AI/personal-memory/internal/todoist"
	"github.com/Dzarlax-AI/personal-memory/internal/viz"
	"github.com/go-chi/chi/v5"
	mcpmiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/mark3labs/mcp-go/server"
)

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpIdleTimeout       = 60 * time.Second
	shutdownTimeout       = 15 * time.Second
	mcpRequestBodyLimit   = 4 << 20
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	slog.Info("starting personal-memory server",
		"port", cfg.Port,
		"todoist", cfg.EnableTodoist,
		"viz", cfg.EnableViz,
	)

	// Init clients.
	qc := qdrant.NewClient(cfg.QdrantURL, "memory")
	ec := embeddings.NewClient(cfg.EmbedURL)

	// Init memory server.
	cache := memory.NewCache(cfg.CacheTTL)
	memSrv := memory.NewServer(qc, ec, cache, cfg.MemoryUser, cfg.DedupThreshold, cfg.ContradictionLow, cfg.MutationMatchThreshold)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	// The recall worker outlives the signal context so active HTTP requests can
	// finish recording recalls during graceful shutdown. It is stopped explicitly
	// after srv.Shutdown has drained handlers.
	memSrv.Start(context.Background())

	// Init Qdrant collection.
	if err := memSrv.InitCollection(ctx); err != nil {
		slog.Error("failed to init collection", "error", err)
		os.Exit(1)
	}
	slog.Info("qdrant collection ready")

	// Create MCP server for memory.
	mcpMemory := server.NewMCPServer("personal-memory", "1.0.0",
		server.WithToolCapabilities(true),
	)
	memSrv.RegisterTools(mcpMemory)

	// Main router.
	r := chi.NewRouter()
	r.Use(mcpmiddleware.Logger)
	r.Use(mcpmiddleware.Recoverer)

	// Health check (no auth).
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	var verifier oauthauth.TokenVerifier
	if cfg.OAuth.Enabled {
		jwksURL := cfg.OAuth.JWKSURL
		if jwksURL == "" {
			discovered, err := oauthauth.DiscoverJWKSURL(ctx, cfg.OAuth.Issuer)
			if err != nil {
				slog.Error("failed to discover OAuth JWKS URL", "error", err)
				os.Exit(1)
			}
			jwksURL = discovered
		}
		var err error
		verifier, err = oauthauth.NewJWTVerifier(oauthauth.JWTVerifierConfig{
			Issuer:   cfg.OAuth.Issuer,
			Audience: cfg.OAuth.Audience,
			JWKSURL:  jwksURL,
			Scopes:   cfg.OAuth.Scopes,
		})
		if err != nil {
			slog.Error("failed to configure OAuth verifier", "error", err)
			os.Exit(1)
		}
		metadata := oauthauth.NewProtectedResourceMetadata(oauthauth.MetadataConfig{
			Resource:              cfg.OAuth.Resource,
			AuthorizationServers:  cfg.OAuth.AuthorizationServers,
			Scopes:                cfg.OAuth.Scopes,
			ResourceDocumentation: cfg.OAuth.ResourceDocumentation,
		})
		r.Get("/.well-known/oauth-protected-resource", oauthauth.MetadataHandler(metadata))
		slog.Info("OAuth MCP auth enabled", "issuer", cfg.OAuth.Issuer, "resource", cfg.OAuth.Resource)
	}

	// RAG MCP (optional).
	var ragSrv *rag.Server
	if cfg.EnableRAG {
		qcChunks := qdrant.NewClient(cfg.QdrantURL, cfg.RAGCollectionChunks)
		qcFolders := qdrant.NewClient(cfg.QdrantURL, cfg.RAGCollectionFolders)
		ragSrv = rag.NewServer(ctx, qcChunks, qcFolders, ec, cfg)
		if err := ragSrv.EnsureCollections(ctx); err != nil {
			slog.Error("failed to init RAG collections", "error", err)
			os.Exit(1)
		}
		ragSrv.RegisterTools(mcpMemory)
		ragSrv.StartAutoReindex(ctx)
		slog.Info("RAG enabled",
			"dir", cfg.RAGDocumentsDir,
			"reindex_interval", cfg.RAGReindexInterval)
	}

	// Memory MCP endpoint accepts API key auth and optional OAuth bearer tokens.
	r.Group(func(r chi.Router) {
		// Auth middleware itself always fails closed on empty configuration.
		// Bypass it only for the explicit development escape hatch; this keeps an
		// accidental empty middleware config from silently becoming public.
		if memoryAuthRequired(cfg.APIKey, cfg.OAuth.Enabled, cfg.AllowInsecureAuth) {
			r.Use(middleware.Auth(middleware.AuthConfig{
				APIKey:        cfg.APIKey,
				OAuthEnabled:  cfg.OAuth.Enabled,
				OAuthResource: cfg.OAuth.Resource,
				OAuthScopes:   cfg.OAuth.Scopes,
				Verifier:      verifier,
			}))
		}
		r.Use(middleware.RequestBodyLimit(mcpRequestBodyLimit))

		memoryHTTP := server.NewStreamableHTTPServer(mcpMemory)
		r.Handle("/memory", memoryHTTP)
		r.Handle("/memory/", memoryHTTP)
		r.Get("/memory/operational", memSrv.OperationalContextHandler())
	})

	// Todoist MCP is registered only when explicitly enabled. Config validation
	// requires its token and API key only behind that feature gate.
	if cfg.EnableTodoist {
		tc := todoist.NewClient(cfg.TodoistToken)
		todoistSrv := todoist.NewServer(tc)
		mcpTodoist := server.NewMCPServer("personal-todoist", "1.0.0",
			server.WithToolCapabilities(true),
		)
		todoistSrv.RegisterTools(mcpTodoist)

		todoistHTTP := server.NewStreamableHTTPServer(mcpTodoist)
		r.Group(func(r chi.Router) {
			r.Use(middleware.APIKeyAuth(cfg.APIKey))
			r.Use(middleware.RequestBodyLimit(mcpRequestBodyLimit))
			r.Handle("/todoist", todoistHTTP)
			r.Handle("/todoist/", todoistHTTP)
		})

		slog.Info("todoist MCP enabled")
	}

	// Viz dashboard (optional). In production, Authentik authenticates the
	// browser and Traefik overwrites the trusted proxy-secret header. The app
	// verifies that secret so direct container/network access fails closed.
	if cfg.EnableViz {
		vizHandler := viz.NewHandler(qc, cfg.VizSimilarityThreshold)
		if cfg.EnableRAG {
			vizChunks := qdrant.NewClient(cfg.QdrantURL, cfg.RAGCollectionChunks)
			vizHandler = vizHandler.WithDocumentRAG(vizChunks, cfg.RAGDocumentsDir)
		}
		if cfg.AllowInsecureAuth && cfg.VizProxySecret == "" {
			r.Mount("/viz", vizHandler.Router())
		} else {
			r.Group(func(r chi.Router) {
				r.Use(middleware.ProxySecretAuth(cfg.VizProxySecret))
				r.Mount("/viz", vizHandler.Router())
			})
		}
		slog.Info("viz dashboard enabled")
	}

	// Start backup loop.
	bl := backup.NewLoop(qc, cfg.BackupInterval, cfg.KeepSnapshots)
	var backgroundWG sync.WaitGroup
	backgroundWG.Add(1)
	go func() {
		defer backgroundWG.Done()
		bl.Run(ctx)
	}()

	// Start HTTP server.
	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		slog.Info("shutting down server")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		if err := shutdownHTTPServer(shutdownCtx, srv); err != nil {
			slog.Error("graceful HTTP shutdown failed", "error", err)
			if closeErr := srv.Close(); closeErr != nil {
				slog.Error("forced HTTP close failed", "error", closeErr)
			}
		}
	}()

	slog.Info("listening", "addr", addr)
	serveErr := srv.ListenAndServe()
	if serveErr != nil && serveErr != http.ErrServerClosed {
		slog.Error("server error", "error", serveErr)
		cancel()
	}
	cancel()
	<-shutdownDone

	workCtx, workCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer workCancel()
	if err := memSrv.Shutdown(workCtx); err != nil {
		slog.Error("memory background shutdown failed", "error", err)
	}
	if ragSrv != nil {
		if err := ragSrv.Wait(workCtx); err != nil {
			slog.Error("RAG background shutdown failed", "error", err)
		}
	}
	if err := waitGroup(workCtx, &backgroundWG); err != nil {
		slog.Error("backup background shutdown failed", "error", err)
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		os.Exit(1)
	}
}

func memoryAuthRequired(apiKey string, oauthEnabled, allowInsecure bool) bool {
	return apiKey != "" || oauthEnabled || !allowInsecure
}

func shutdownHTTPServer(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

func waitGroup(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
