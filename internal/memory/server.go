package memory

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	qdrant           *qdrant.Client
	embed            *embeddings.Client
	cache            *Cache
	user             string
	dedupThreshold   float64
	contradictionLow float64
}

func NewServer(qc *qdrant.Client, ec *embeddings.Client, cache *Cache, user string, dedupThreshold, contradictionLow float64) *Server {
	return &Server{
		qdrant:           qc,
		embed:            ec,
		cache:            cache,
		user:             user,
		dedupThreshold:   dedupThreshold,
		contradictionLow: contradictionLow,
	}
}

// InitCollection creates the Qdrant collection if missing.
func (s *Server) InitCollection(ctx context.Context) error {
	vec, err := s.embed.Embed(ctx, "init")
	if err != nil {
		return fmt.Errorf("init embed: %w", err)
	}
	return s.qdrant.EnsureCollection(ctx, len(vec))
}

// RegisterTools registers all memory MCP tools on the given MCP server.
func (s *Server) RegisterTools(srv *server.MCPServer) {
	srv.AddTool(mcp.NewTool("store_fact",
		mcp.WithDescription("Store a fact in semantic memory. Deduplicates (cosine >= threshold) and warns on contradictions."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("fact", mcp.Description("The fact to store"), mcp.Required()),
		mcp.WithString("tags", mcp.Description("Comma-separated semantic tags")),
		mcp.WithString("primary_tag", mcp.Description("Single primary tag for overview grouping; must also be present in tags")),
		mcp.WithString("namespace", mcp.Description("Namespace (default: default)")),
		mcp.WithBoolean("permanent", mcp.Description("Never deleted by forget_old")),
		mcp.WithString("valid_until", mcp.Description("ISO date after which fact expires")),
	), s.storeFact)

	srv.AddTool(mcp.NewTool("recall_facts",
		mcp.WithDescription("Semantic search for facts. Returns facts with relevance scores."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query", mcp.Description("Natural language search query"), mcp.Required()),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 5)")),
	), s.recallFacts)

	srv.AddTool(mcp.NewTool("update_fact",
		mcp.WithDescription("Find a fact by similarity to old_query and replace it with new_fact."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("old_query", mcp.Description("Query to find the fact to update"), mcp.Required()),
		mcp.WithString("new_fact", mcp.Description("New fact text"), mcp.Required()),
		mcp.WithString("tags", mcp.Description("Comma-separated semantic tags")),
		mcp.WithString("primary_tag", mcp.Description("Single primary tag for overview grouping; must also be present in tags")),
		mcp.WithString("namespace", mcp.Description("Namespace")),
		mcp.WithBoolean("permanent", mcp.Description("Set permanent flag")),
	), s.updateFact)

	srv.AddTool(mcp.NewTool("delete_fact",
		mcp.WithDescription("Find a fact by similarity and delete it."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query", mcp.Description("Query to find the fact to delete"), mcp.Required()),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
	), s.deleteFact)

	srv.AddTool(mcp.NewTool("forget_old",
		mcp.WithDescription("Delete facts older than N days. Skips permanent facts. Defaults to dry run."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithNumber("days", mcp.Description("Age threshold in days (default 90)")),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
		mcp.WithBoolean("dry_run", mcp.Description("If true, only report what would be deleted (default true)")),
	), s.forgetOld)

	srv.AddTool(mcp.NewTool("import_facts",
		mcp.WithDescription("Bulk import facts from JSON array."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("facts", mcp.Description("JSON array of fact objects"), mcp.Required()),
	), s.importFacts)

	srv.AddTool(mcp.NewTool("find_related",
		mcp.WithDescription("Find related but non-duplicate facts (score 0.60-0.97)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query", mcp.Description("Search query"), mcp.Required()),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 5)")),
	), s.findRelated)

	srv.AddTool(mcp.NewTool("list_facts",
		mcp.WithDescription("List all facts with metadata."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
	), s.listFacts)

	srv.AddTool(mcp.NewTool("get_stats",
		mcp.WithDescription("Get memory statistics: counts, namespaces, tags, most recalled."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), s.getStats)

	srv.AddTool(mcp.NewTool("list_tags",
		mcp.WithDescription("List all tags with counts."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
	), s.listTags)

	srv.AddTool(mcp.NewTool("export_facts",
		mcp.WithDescription("Export all facts as JSON."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
	), s.exportFacts)

	srv.AddTool(mcp.NewTool("get_operational_context",
		mcp.WithDescription("Return operational context: all permanent facts plus top facts by recall count. Call at session start for automatic context loading."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("namespace", mcp.Description("Filter by namespace")),
		mcp.WithNumber("top_recalled", mcp.Description("Number of top recalled non-permanent facts to include (default 10)")),
	), s.getOperationalContext)
}

// --- Tool parameter helpers ---

func strParam(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func intParam(args map[string]interface{}, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return def
}

func boolParam(args map[string]interface{}, key string, def bool) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	b, _ := v.(bool)
	return b
}

func tagsParam(args map[string]interface{}) []string {
	v, ok := args["tags"]
	if !ok || v == nil {
		return nil
	}
	return tagsParamFromPayload(v)
}

func tagsParamFromPayload(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		tags := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				tags = append(tags, s)
			}
		}
		return tags
	case string:
		if t == "" {
			return nil
		}
		return strings.Split(t, ",")
	}
	return nil
}

func stringFromPayload(v interface{}) string {
	s, _ := v.(string)
	return s
}

// --- Helpers ---

func normalizeFactTags(tags []string, primary string) ([]string, string) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}

	primary = strings.TrimSpace(primary)
	if primary != "" {
		if _, ok := seen[primary]; !ok {
			out = append(out, primary)
		}
		return out, primary
	}
	if len(out) == 1 {
		return out, out[0]
	}
	return out, ""
}

func pointID(text string) string {
	h := sha1.New()
	h.Write([]byte(text))
	b := h.Sum(nil)
	// Format as UUID v5 style: 8-4-4-4-12
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func isExpired(payload map[string]interface{}) bool {
	v, ok := payload["valid_until"]
	if !ok || v == nil {
		return false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return false
	}
	return time.Now().After(t)
}

func (s *Server) buildFilters(tags []string, namespace string) map[string]interface{} {
	var must []map[string]interface{}
	if namespace != "" {
		must = append(must, map[string]interface{}{
			"key": "namespace",
			"match": map[string]interface{}{
				"value": namespace,
			},
		})
	}
	for _, tag := range tags {
		must = append(must, map[string]interface{}{
			"key": "tags",
			"match": map[string]interface{}{
				"value": tag,
			},
		})
	}
	if len(must) == 0 {
		return nil
	}
	return map[string]interface{}{
		"must": must,
	}
}

// --- Tool implementations ---

func (s *Server) storeFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	fact := strParam(args, "fact")
	if fact == "" {
		return mcp.NewToolResultError("fact is required"), nil
	}
	tags, primaryTag := normalizeFactTags(tagsParam(args), strParam(args, "primary_tag"))
	namespace := strParam(args, "namespace")
	if namespace == "" {
		namespace = "default"
	}
	permanent := boolParam(args, "permanent", false)
	validUntil := strParam(args, "valid_until")

	vec, err := s.embed.Embed(ctx, fact)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	// Check for duplicates and contradictions.
	existing, err := s.qdrant.Search(ctx, vec, 3, s.buildFilters(nil, namespace), nil)
	if err != nil {
		slog.Warn("dedup search failed", "error", err)
	}

	for _, p := range existing {
		if p.Score >= s.dedupThreshold {
			existingText, _ := p.Payload["text"].(string)
			return mcp.NewToolResultText(fmt.Sprintf("⚠️ Duplicate (score %.2f): %s", p.Score, existingText)), nil
		}
	}

	var warnings []string
	for _, p := range existing {
		if p.Score >= s.contradictionLow && p.Score < s.dedupThreshold {
			existingText, _ := p.Payload["text"].(string)
			warnings = append(warnings, fmt.Sprintf("⚠️ Possible contradiction (score %.2f): %s", p.Score, existingText))
		}
	}

	payload := map[string]interface{}{
		"text":         fact,
		"user":         s.user,
		"namespace":    namespace,
		"tags":         tags,
		"primary_tag":  primaryTag,
		"permanent":    permanent,
		"created_at":   nowISO(),
		"recall_count": 0,
	}
	if validUntil != "" {
		payload["valid_until"] = validUntil
	}

	if err := s.qdrant.Upsert(ctx, qdrant.Point{
		ID:      pointID(fact),
		Vector:  vec,
		Payload: payload,
	}); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("store failed: %v", err)), nil
	}

	s.cache.Invalidate()

	result := fmt.Sprintf("Stored: %s", fact)
	if len(warnings) > 0 {
		result += "\n" + strings.Join(warnings, "\n")
	}
	return mcp.NewToolResultText(result), nil
}

func (s *Server) recallFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query := strParam(args, "query")
	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}
	tags := tagsParam(args)
	namespace := strParam(args, "namespace")
	limit := intParam(args, "limit", 5)

	cacheKey := fmt.Sprintf("%s|%s|%v|%d", query, namespace, tags, limit)
	if cached, ok := s.cache.Get(cacheKey); ok {
		return mcp.NewToolResultText(formatFacts(cached)), nil
	}

	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	results, err := s.qdrant.Search(ctx, vec, limit*2, s.buildFilters(tags, namespace), nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	var hits []map[string]interface{}
	for _, p := range results {
		if isExpired(p.Payload) {
			continue
		}
		hit := map[string]interface{}{
			"text":         p.Payload["text"],
			"score":        p.Score,
			"tags":         p.Payload["tags"],
			"primary_tag":  p.Payload["primary_tag"],
			"namespace":    p.Payload["namespace"],
			"recall_count": p.Payload["recall_count"],
		}
		hits = append(hits, hit)

		// Async update recall_count.
		go func(id string, payload map[string]interface{}) {
			rc := 0
			if v, ok := payload["recall_count"].(float64); ok {
				rc = int(v)
			}
			_ = s.qdrant.SetPayload(context.Background(), id, map[string]interface{}{
				"recall_count":     rc + 1,
				"last_recalled_at": nowISO(),
			})
		}(p.ID, p.Payload)

		if len(hits) >= limit {
			break
		}
	}

	s.cache.Set(cacheKey, hits)
	return mcp.NewToolResultText(formatFacts(hits)), nil
}

func (s *Server) updateFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	oldQuery := strParam(args, "old_query")
	newFact := strParam(args, "new_fact")
	if oldQuery == "" || newFact == "" {
		return mcp.NewToolResultError("old_query and new_fact are required"), nil
	}
	namespace := strParam(args, "namespace")

	vec, err := s.embed.Embed(ctx, oldQuery)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	results, err := s.qdrant.Search(ctx, vec, 1, s.buildFilters(nil, namespace), nil)
	if err != nil || len(results) == 0 {
		return mcp.NewToolResultError("no matching fact found"), nil
	}

	old := results[0]
	oldText, _ := old.Payload["text"].(string)
	newID := pointID(newFact)

	// Embed new fact.
	newVec, err := s.embed.Embed(ctx, newFact)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding new fact failed: %v", err)), nil
	}

	// Preserve metadata from old fact.
	payload := old.Payload
	payload["text"] = newFact
	payload["updated_at"] = nowISO()
	if ns := strParam(args, "namespace"); ns != "" {
		payload["namespace"] = ns
	}
	if tags := tagsParam(args); tags != nil {
		primary := strParam(args, "primary_tag")
		if primary == "" {
			primary = stringFromPayload(payload["primary_tag"])
		}
		normalizedTags, primaryTag := normalizeFactTags(tags, primary)
		payload["tags"] = normalizedTags
		payload["primary_tag"] = primaryTag
	} else if primary := strParam(args, "primary_tag"); primary != "" {
		normalizedTags, primaryTag := normalizeFactTags(tagsParamFromPayload(payload["tags"]), primary)
		payload["tags"] = normalizedTags
		payload["primary_tag"] = primaryTag
	}
	if v, ok := args["permanent"]; ok && v != nil {
		payload["permanent"] = v
	}

	if err := s.qdrant.Upsert(ctx, qdrant.Point{
		ID:      newID,
		Vector:  newVec,
		Payload: payload,
	}); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("store updated fact failed: %v", err)), nil
	}

	if old.ID != newID {
		if err := s.qdrant.Delete(ctx, []string{old.ID}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete old failed: %v", err)), nil
		}
	}

	s.cache.Invalidate()
	return mcp.NewToolResultText(fmt.Sprintf("Updated: '%s' → '%s'", oldText, newFact)), nil
}

func (s *Server) deleteFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query := strParam(args, "query")
	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}
	namespace := strParam(args, "namespace")

	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	results, err := s.qdrant.Search(ctx, vec, 1, s.buildFilters(nil, namespace), nil)
	if err != nil || len(results) == 0 {
		return mcp.NewToolResultError("no matching fact found"), nil
	}

	target := results[0]
	if err := s.qdrant.Delete(ctx, []string{target.ID}); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("delete failed: %v", err)), nil
	}

	s.cache.Invalidate()
	text, _ := target.Payload["text"].(string)
	return mcp.NewToolResultText(fmt.Sprintf("Deleted: %s (score %.2f)", text, target.Score)), nil
}

func (s *Server) forgetOld(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	days := intParam(args, "days", 90)
	namespace := strParam(args, "namespace")
	dryRun := boolParam(args, "dry_run", true)

	cutoff := time.Now().AddDate(0, 0, -days)
	filters := s.buildFilters(nil, namespace)

	points, err := s.qdrant.ScrollAll(ctx, filters, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	var toDelete []string
	var details []string
	for _, p := range points {
		if perm, ok := p.Payload["permanent"].(bool); ok && perm {
			continue
		}
		createdStr, _ := p.Payload["created_at"].(string)
		created, err := time.Parse(time.RFC3339, createdStr)
		if err != nil {
			continue
		}
		if created.Before(cutoff) {
			text, _ := p.Payload["text"].(string)
			toDelete = append(toDelete, p.ID)
			details = append(details, fmt.Sprintf("- %s (created %s)", text, createdStr))
		}
	}

	if dryRun {
		if len(toDelete) == 0 {
			return mcp.NewToolResultText("Dry run: nothing to delete."), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Dry run: would delete %d facts:\n%s", len(toDelete), strings.Join(details, "\n"))), nil
	}

	if len(toDelete) == 0 {
		return mcp.NewToolResultText("Nothing to delete."), nil
	}

	if err := s.qdrant.Delete(ctx, toDelete); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("delete failed: %v", err)), nil
	}

	s.cache.Invalidate()
	return mcp.NewToolResultText(fmt.Sprintf("Deleted %d facts.", len(toDelete))), nil
}

func (s *Server) importFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	factsRaw := strParam(args, "facts")
	if factsRaw == "" {
		return mcp.NewToolResultError("facts is required"), nil
	}

	// Parse JSON array of fact objects.
	var facts []map[string]interface{}
	if err := json.Unmarshal([]byte(factsRaw), &facts); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid JSON: %v", err)), nil
	}

	imported := 0
	skipped := 0
	for _, f := range facts {
		text, _ := f["text"].(string)
		if text == "" {
			skipped++
			continue
		}

		vec, err := s.embed.Embed(ctx, text)
		if err != nil {
			slog.Warn("import embed failed", "text", text, "error", err)
			skipped++
			continue
		}

		// Dedup check.
		existing, _ := s.qdrant.Search(ctx, vec, 1, nil, nil)
		if len(existing) > 0 && existing[0].Score >= s.dedupThreshold {
			skipped++
			continue
		}

		namespace, _ := f["namespace"].(string)
		if namespace == "" {
			namespace = "default"
		}

		payload := map[string]interface{}{
			"text":         text,
			"user":         s.user,
			"namespace":    namespace,
			"tags":         nil,
			"primary_tag":  nil,
			"permanent":    f["permanent"],
			"created_at":   f["created_at"],
			"recall_count": 0,
		}
		tags, primaryTag := normalizeFactTags(tagsParamFromPayload(f["tags"]), stringFromPayload(f["primary_tag"]))
		payload["tags"] = tags
		payload["primary_tag"] = primaryTag
		if v, ok := f["valid_until"]; ok {
			payload["valid_until"] = v
		}
		if payload["created_at"] == nil {
			payload["created_at"] = nowISO()
		}

		if err := s.qdrant.Upsert(ctx, qdrant.Point{
			ID:      pointID(text),
			Vector:  vec,
			Payload: payload,
		}); err != nil {
			slog.Warn("import upsert failed", "text", text, "error", err)
			skipped++
			continue
		}
		imported++
	}

	s.cache.Invalidate()
	return mcp.NewToolResultText(fmt.Sprintf("Imported %d facts, skipped %d.", imported, skipped)), nil
}

func (s *Server) findRelated(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query := strParam(args, "query")
	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}
	namespace := strParam(args, "namespace")
	limit := intParam(args, "limit", 5)

	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	low := s.contradictionLow
	results, err := s.qdrant.Search(ctx, vec, limit*3, s.buildFilters(nil, namespace), &low)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	var hits []map[string]interface{}
	for _, p := range results {
		if p.Score >= s.dedupThreshold {
			continue // skip near-duplicates
		}
		if isExpired(p.Payload) {
			continue
		}
		hits = append(hits, map[string]interface{}{
			"text":        p.Payload["text"],
			"score":       p.Score,
			"tags":        p.Payload["tags"],
			"primary_tag": p.Payload["primary_tag"],
			"namespace":   p.Payload["namespace"],
		})
		if len(hits) >= limit {
			break
		}
	}

	return mcp.NewToolResultText(formatFacts(hits)), nil
}

func (s *Server) listFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	namespace := strParam(args, "namespace")
	tags := tagsParam(args)

	filters := s.buildFilters(tags, namespace)
	points, err := s.qdrant.ScrollAll(ctx, filters, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	var lines []string
	for _, p := range points {
		text, _ := p.Payload["text"].(string)
		ns, _ := p.Payload["namespace"].(string)
		createdAt, _ := p.Payload["created_at"].(string)
		rc := 0
		if v, ok := p.Payload["recall_count"].(float64); ok {
			rc = int(v)
		}
		perm := ""
		if v, ok := p.Payload["permanent"].(bool); ok && v {
			perm = " [permanent]"
		}
		tagsList := formatTagsList(p.Payload["tags"])
		primary := formatPrimaryTag(p.Payload["primary_tag"])
		lines = append(lines, fmt.Sprintf("- [%s] %s%s ns:%s%s recalls:%d %s", createdAt, tagsList, primary, ns, perm, rc, text))
	}

	if len(lines) == 0 {
		return mcp.NewToolResultText("No facts found."), nil
	}
	return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (s *Server) getStats(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	points, err := s.qdrant.ScrollAll(ctx, nil, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	total := len(points)
	permanent := 0
	expired := 0
	namespaces := make(map[string]int)
	tags := make(map[string]int)
	primaryTags := make(map[string]int)
	missingPrimary := 0
	var mostRecalled string
	maxRecalls := 0

	for _, p := range points {
		if v, ok := p.Payload["permanent"].(bool); ok && v {
			permanent++
		}
		if isExpired(p.Payload) {
			expired++
		}
		if ns, ok := p.Payload["namespace"].(string); ok {
			namespaces[ns]++
		}
		for _, tag := range tagsParamFromPayload(p.Payload["tags"]) {
			tags[tag]++
		}
		if primary, ok := p.Payload["primary_tag"].(string); ok && strings.TrimSpace(primary) != "" {
			primaryTags[primary]++
		} else {
			missingPrimary++
		}
		if rc, ok := p.Payload["recall_count"].(float64); ok && int(rc) > maxRecalls {
			maxRecalls = int(rc)
			mostRecalled, _ = p.Payload["text"].(string)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Total facts: %d\n", total)
	fmt.Fprintf(&sb, "Permanent: %d\n", permanent)
	fmt.Fprintf(&sb, "Expired: %d\n", expired)

	sb.WriteString("\nNamespaces:\n")
	for ns, count := range namespaces {
		fmt.Fprintf(&sb, "  %s: %d\n", ns, count)
	}

	sb.WriteString("\nTop tags:\n")
	type tagCount struct {
		tag   string
		count int
	}
	var sorted []tagCount
	for t, c := range tags {
		sorted = append(sorted, tagCount{t, c})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
	for i, tc := range sorted {
		if i >= 20 {
			break
		}
		fmt.Fprintf(&sb, "  %s: %d\n", tc.tag, tc.count)
	}

	sb.WriteString("\nPrimary tags:\n")
	sorted = sorted[:0]
	for t, c := range primaryTags {
		sorted = append(sorted, tagCount{t, c})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
	for i, tc := range sorted {
		if i >= 20 {
			break
		}
		fmt.Fprintf(&sb, "  %s: %d\n", tc.tag, tc.count)
	}
	fmt.Fprintf(&sb, "  no primary_tag: %d\n", missingPrimary)

	if mostRecalled != "" {
		fmt.Fprintf(&sb, "\nMost recalled (%d times): %s", maxRecalls, mostRecalled)
	}

	return mcp.NewToolResultText(sb.String()), nil
}

func (s *Server) listTags(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	namespace := strParam(args, "namespace")

	filters := s.buildFilters(nil, namespace)
	points, err := s.qdrant.ScrollAll(ctx, filters, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	tags := make(map[string]int)
	for _, p := range points {
		for _, tag := range tagsParamFromPayload(p.Payload["tags"]) {
			tags[tag]++
		}
	}

	if len(tags) == 0 {
		return mcp.NewToolResultText("No tags found."), nil
	}

	var lines []string
	for tag, count := range tags {
		lines = append(lines, fmt.Sprintf("%s: %d", tag, count))
	}
	sort.Strings(lines)
	return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (s *Server) exportFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	namespace := strParam(args, "namespace")

	filters := s.buildFilters(nil, namespace)
	points, err := s.qdrant.ScrollAll(ctx, filters, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	var facts []map[string]interface{}
	for _, p := range points {
		facts = append(facts, p.Payload)
	}

	b, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal failed: %v", err)), nil
	}

	return mcp.NewToolResultText(string(b)), nil
}

func (s *Server) getOperationalFacts(ctx context.Context, namespace string, topRecalled int) ([]qdrant.ScrollPoint, error) {
	filters := s.buildFilters(nil, namespace)

	points, err := s.qdrant.ScrollAll(ctx, filters, false)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var permanent []qdrant.ScrollPoint
	var nonPermanent []qdrant.ScrollPoint

	for _, p := range points {
		if isExpired(p.Payload) {
			continue
		}
		if v, ok := p.Payload["permanent"].(bool); ok && v {
			permanent = append(permanent, p)
			seen[p.ID] = true
		} else {
			nonPermanent = append(nonPermanent, p)
		}
	}

	// Sort non-permanent by recall_count desc, take top N.
	sort.Slice(nonPermanent, func(i, j int) bool {
		ri, _ := nonPermanent[i].Payload["recall_count"].(float64)
		rj, _ := nonPermanent[j].Payload["recall_count"].(float64)
		return ri > rj
	})

	result := permanent
	added := 0
	for _, p := range nonPermanent {
		if added >= topRecalled {
			break
		}
		rc, _ := p.Payload["recall_count"].(float64)
		if rc == 0 {
			break // no point including never-recalled facts
		}
		if !seen[p.ID] {
			result = append(result, p)
			added++
		}
	}

	return result, nil
}

func (s *Server) getOperationalContext(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	namespace := strParam(args, "namespace")
	topRecalled := intParam(args, "top_recalled", 10)

	points, err := s.getOperationalFacts(ctx, namespace, topRecalled)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	if len(points) == 0 {
		return mcp.NewToolResultText("No operational context found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Operational Context (%d facts)\n\n", len(points))
	for _, p := range points {
		text, _ := p.Payload["text"].(string)
		ns, _ := p.Payload["namespace"].(string)
		perm := ""
		if v, ok := p.Payload["permanent"].(bool); ok && v {
			perm = " [permanent]"
		}
		rc := 0
		if v, ok := p.Payload["recall_count"].(float64); ok {
			rc = int(v)
		}
		tagsList := formatTagsList(p.Payload["tags"])
		primary := formatPrimaryTag(p.Payload["primary_tag"])
		fmt.Fprintf(&sb, "- %s%s ns:%s%s recalls:%d %s\n", tagsList, primary, ns, perm, rc, text)
	}
	return mcp.NewToolResultText(sb.String()), nil
}

// OperationalContextHandler returns an HTTP handler for GET /memory/operational.
// Returns operational facts as plain text, suitable for Claude Code hooks.
func (s *Server) OperationalContextHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		points, err := s.getOperationalFacts(r.Context(), r.URL.Query().Get("namespace"), 10)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(points) == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		var sb strings.Builder
		sb.WriteString("# Operational Context\n\n")
		for _, p := range points {
			text, _ := p.Payload["text"].(string)
			ns, _ := p.Payload["namespace"].(string)
			fmt.Fprintf(&sb, "- [%s] %s\n", ns, text)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(sb.String()))
	}
}

// --- Formatting helpers ---

func formatFacts(hits []map[string]interface{}) string {
	if len(hits) == 0 {
		return "No facts found."
	}
	var lines []string
	for _, h := range hits {
		text, _ := h["text"].(string)
		ns, _ := h["namespace"].(string)
		tagsList := formatTagsList(h["tags"])
		primary := formatPrimaryTag(h["primary_tag"])

		line := fmt.Sprintf("- [%.3f] %s%s ns:%s %s", h["score"], tagsList, primary, ns, text)
		if rc, ok := h["recall_count"].(float64); ok && rc > 0 {
			line = fmt.Sprintf("- [%.3f] %s%s ns:%s recalls:%.0f %s", h["score"], tagsList, primary, ns, rc, text)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatTagsList(v interface{}) string {
	if v == nil {
		return "[]"
	}
	switch t := v.(type) {
	case []interface{}:
		tags := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				tags = append(tags, "'"+s+"'")
			}
		}
		return "[" + strings.Join(tags, ", ") + "]"
	case []string:
		tags := make([]string, 0, len(t))
		for _, s := range t {
			tags = append(tags, "'"+s+"'")
		}
		return "[" + strings.Join(tags, ", ") + "]"
	}
	return "[]"
}

func formatPrimaryTag(v interface{}) string {
	primary, _ := v.(string)
	if primary == "" {
		return ""
	}
	return fmt.Sprintf(" primary:%s", primary)
}
