package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	qdrant                 *qdrant.Client
	embed                  *embeddings.Client
	cache                  *Cache
	user                   string
	dedupThreshold         float64
	relatedFactLow         float64
	mutationMatchThreshold float64
	recallCounterMu        sync.Mutex
	recallCounter          *recallCounter
}

// Start starts the bounded recall-counter worker. It is safe to call once.
func (s *Server) Start(ctx context.Context) {
	s.recallCounterMu.Lock()
	defer s.recallCounterMu.Unlock()
	if s.recallCounter == nil {
		s.recallCounter = newRecallCounter(ctx, s.qdrant, defaultRecallQueueSize, defaultRecallFlushInterval)
	}
}

// Shutdown stops accepting recall increments and drains queued work.
func (s *Server) Shutdown(ctx context.Context) error {
	s.recallCounterMu.Lock()
	counter := s.recallCounter
	s.recallCounterMu.Unlock()
	if counter == nil {
		return nil
	}
	return counter.stop(ctx)
}

func (s *Server) countRecalls(ctx context.Context, hits []map[string]interface{}) error {
	s.recallCounterMu.Lock()
	counter := s.recallCounter
	s.recallCounterMu.Unlock()
	if counter == nil {
		return fmt.Errorf("recall counter is not running")
	}
	for _, hit := range hits {
		id, _ := hit["_point_id"].(string)
		if id != "" {
			if err := counter.enqueue(ctx, id); err != nil {
				return err
			}
			hit["recall_count"] = payloadInt(hit["recall_count"]) + 1
		}
	}
	return nil
}

const (
	mutationAmbiguityMargin = 0.01
	maxSearchLimit          = 100
	maxFactBytes            = 64 << 10
	maxQueryBytes           = 16 << 10
	maxImportBytes          = 4 << 20
	maxImportFacts          = 1000
	maxNamespaceBytes       = 255
	maxTags                 = 100
	maxTagBytes             = 255
	relatedFactResultLimit  = 3
)

var validPointIDPattern = regexp.MustCompile(`^(?:[0-9]+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

func NewServer(qc *qdrant.Client, ec *embeddings.Client, cache *Cache, user string, dedupThreshold, relatedFactLow, mutationMatchThreshold float64) *Server {
	return &Server{
		qdrant:                 qc,
		embed:                  ec,
		cache:                  cache,
		user:                   user,
		dedupThreshold:         dedupThreshold,
		relatedFactLow:         relatedFactLow,
		mutationMatchThreshold: mutationMatchThreshold,
	}
}

// RegisterTools registers all memory MCP tools on the given MCP server.
func (s *Server) RegisterTools(srv *server.MCPServer) {
	srv.AddTool(mcp.NewTool("store_fact",
		mcp.WithDescription("Store a fact in semantic memory. Cosine similarity identifies related candidates and prevents duplicate writes at the deduplication threshold; valid superseded facts remain related context and do not block storage."),
		mcp.WithOutputSchema[StoreFactResult](),
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
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
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
		mcp.WithString("old_query", mcp.Description("Query to find the fact to update; required unless point_id is set")),
		mcp.WithString("point_id", mcp.Description("Exact fact ID; bypasses similarity matching after namespace validation")),
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
		mcp.WithString("query", mcp.Description("Query to find the fact to delete; required unless point_id is set")),
		mcp.WithString("point_id", mcp.Description("Exact fact ID; bypasses similarity matching after namespace validation")),
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
		mcp.WithDescription("Find lifecycle-ranked related candidates by cosine similarity. Blocking duplicate candidates are excluded, while valid superseded candidates remain inspectable at any qualifying score."),
		mcp.WithOutputSchema[FindRelatedResult](),
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

func intParam(args map[string]interface{}, key string, def int) (int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n > float64(^uint(0)>>1) || n < -float64(^uint(0)>>1)-1 {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(n), nil
	case int:
		return n, nil
	}
	return 0, fmt.Errorf("%s must be an integer", key)
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

func validateBoundedString(name, value string, maxBytes int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s must be at most %d bytes", name, maxBytes)
	}
	return nil
}

func validateNamespace(namespace string) error {
	return validateBoundedString("namespace", namespace, maxNamespaceBytes, false)
}

func validateTagsPayload(raw interface{}) error {
	var tags []string
	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		if value == "" {
			return nil
		}
		tags = strings.Split(value, ",")
	case []string:
		tags = value
	case []interface{}:
		if len(value) > maxTags {
			return fmt.Errorf("tags must contain at most %d entries", maxTags)
		}
		tags = make([]string, 0, len(value))
		for i, item := range value {
			tag, ok := item.(string)
			if !ok {
				return fmt.Errorf("tags[%d] must be a string", i)
			}
			tags = append(tags, tag)
		}
	default:
		return fmt.Errorf("tags must be a comma-separated string or array of strings")
	}
	if len(tags) > maxTags {
		return fmt.Errorf("tags must contain at most %d entries", maxTags)
	}
	for i, tag := range tags {
		if len(tag) > maxTagBytes {
			return fmt.Errorf("tags[%d] must be at most %d bytes", i, maxTagBytes)
		}
	}
	return nil
}

func validateCommonMetadata(args map[string]interface{}) error {
	if err := validateNamespace(strParam(args, "namespace")); err != nil {
		return err
	}
	if raw, ok := args["tags"]; ok {
		if err := validateTagsPayload(raw); err != nil {
			return err
		}
	}
	if err := validateBoundedString("primary_tag", strParam(args, "primary_tag"), maxTagBytes, false); err != nil {
		return err
	}
	return nil
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

func validatePositiveLimit(name string, value int) string {
	if value <= 0 {
		return fmt.Sprintf("%s must be greater than zero", name)
	}
	if value > maxSearchLimit {
		return fmt.Sprintf("%s must be at most %d", name, maxSearchLimit)
	}
	return ""
}

func mutationCandidates(points []qdrant.Point) string {
	parts := make([]string, 0, len(points))
	for _, point := range points {
		text, _ := point.Payload["text"].(string)
		parts = append(parts, fmt.Sprintf("id=%s score=%.3f text=%q", point.ID, point.Score, text))
	}
	return strings.Join(parts, "; ")
}

func (s *Server) mutationTarget(ctx context.Context, args map[string]interface{}, queryKey string) (qdrant.Point, string) {
	namespace := strings.TrimSpace(strParam(args, "namespace"))
	if id := strings.TrimSpace(strParam(args, "point_id")); id != "" {
		if !validPointIDPattern.MatchString(id) {
			return qdrant.Point{}, "point_id must be a numeric legacy ID or UUID"
		}
		point, found, err := s.qdrant.Get(ctx, id)
		if err != nil {
			return qdrant.Point{}, fmt.Sprintf("exact point lookup failed: %v", err)
		}
		if !found {
			return qdrant.Point{}, fmt.Sprintf("no fact found with point_id %s", id)
		}
		if namespace != "" && stringFromPayload(point.Payload["namespace"]) != namespace {
			return qdrant.Point{}, fmt.Sprintf("point_id %s does not belong to namespace %q", id, namespace)
		}
		return point, ""
	}

	query := strings.TrimSpace(strParam(args, queryKey))
	if query == "" {
		return qdrant.Point{}, fmt.Sprintf("%s or point_id is required", queryKey)
	}
	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return qdrant.Point{}, fmt.Sprintf("embedding failed: %v", err)
	}
	results, err := s.qdrant.Search(ctx, vec, 2, s.buildFilters(nil, namespace), nil)
	if err != nil {
		return qdrant.Point{}, fmt.Sprintf("search failed: %v", err)
	}
	if len(results) == 0 {
		return qdrant.Point{}, "no matching fact found"
	}
	if results[0].Score < s.mutationMatchThreshold {
		return qdrant.Point{}, fmt.Sprintf("mutation refused: best score %.3f is below threshold %.3f; candidates: %s", results[0].Score, s.mutationMatchThreshold, mutationCandidates(results))
	}
	if len(results) > 1 && results[0].Score-results[1].Score < mutationAmbiguityMargin {
		return qdrant.Point{}, fmt.Sprintf("mutation refused: ambiguous matches (score delta %.3f is below %.3f); candidates: %s", results[0].Score-results[1].Score, mutationAmbiguityMargin, mutationCandidates(results))
	}
	return results[0], ""
}

// --- Tool implementations ---

type StoreFactResult struct {
	Status       string                 `json:"status"`
	Stored       bool                   `json:"stored"`
	PointID      string                 `json:"point_id,omitempty"`
	Duplicate    *RelatedFactCandidate  `json:"duplicate,omitempty"`
	RelatedFacts []RelatedFactCandidate `json:"related_facts"`
}

type FindRelatedResult struct {
	RelatedFacts []RelatedFactCandidate `json:"related_facts"`
	Count        int                    `json:"count"`
}

func formatRelatedCandidate(candidate RelatedFactCandidate) string {
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return "- candidate: <unavailable>"
	}
	return "- candidate: " + string(encoded)
}

func formatStoreFactResult(result StoreFactResult) string {
	lines := []string{
		"status: " + result.Status,
		fmt.Sprintf("stored: %t", result.Stored),
	}
	if result.PointID != "" {
		lines = append(lines, "point_id: "+result.PointID)
	}
	if result.Duplicate != nil {
		lines = append(lines, "duplicate:", formatRelatedCandidate(*result.Duplicate))
	}
	lines = append(lines, fmt.Sprintf("related_facts: %d", len(result.RelatedFacts)))
	for _, candidate := range result.RelatedFacts {
		lines = append(lines, formatRelatedCandidate(candidate))
	}
	return strings.Join(lines, "\n")
}

func formatFindRelatedResult(result FindRelatedResult) string {
	lines := []string{fmt.Sprintf("count: %d", result.Count)}
	for _, candidate := range result.RelatedFacts {
		lines = append(lines, formatRelatedCandidate(candidate))
	}
	return strings.Join(lines, "\n")
}

func (s *Server) storeFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	fact := strParam(args, "fact")
	if err := validateBoundedString("fact", fact, maxFactBytes, true); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validateCommonMetadata(args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	tags, primaryTag := normalizeFactTags(tagsParam(args), strParam(args, "primary_tag"))
	namespace := NormalizeNamespace(strParam(args, "namespace"))
	permanent := boolParam(args, "permanent", false)
	validUntil := strParam(args, "valid_until")
	if validUntil != "" {
		if _, err := time.Parse("2006-01-02", validUntil); err != nil {
			return mcp.NewToolResultError("valid_until must use YYYY-MM-DD format"), nil
		}
	}

	vec, err := s.embed.Embed(ctx, fact)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	dedupLimit := lifecycleCandidateLimit(relatedFactResultLimit)
	dedupLow := s.dedupThreshold
	dedupCandidates, dedupErr := s.qdrant.Search(ctx, vec, dedupLimit, s.buildFilters(nil, namespace), &dedupLow)
	var duplicate *RelatedFactCandidate
	if dedupErr != nil {
		// Preserve the existing fail-open behavior: availability of duplicate
		// preflight must not make the memory write path unavailable.
		slog.Warn("dedup search failed", "error", dedupErr)
	} else {
		duplicate, _ = selectRelatedCandidates(dedupCandidates, s.relatedFactLow, s.dedupThreshold, relatedFactResultLimit)
		if duplicate == nil && len(dedupCandidates) == dedupLimit {
			return mcp.NewToolResultError("duplicate preflight inconclusive; candidate limit reached"), nil
		}
	}

	relatedFacts := []RelatedFactCandidate{}
	relatedLow := s.relatedFactLow
	relatedCandidates, relatedErr := s.qdrant.Search(ctx, vec, lifecycleCandidateLimit(relatedFactResultLimit), s.buildFilters(nil, namespace), &relatedLow)
	if relatedErr != nil {
		slog.Warn("related fact search failed", "error", relatedErr)
	} else {
		var relatedDuplicate *RelatedFactCandidate
		relatedDuplicate, relatedFacts = selectRelatedCandidates(relatedCandidates, s.relatedFactLow, s.dedupThreshold, relatedFactResultLimit)
		if duplicate == nil {
			duplicate = relatedDuplicate
		}
	}

	if duplicate != nil {
		result := StoreFactResult{
			Status:       "duplicate",
			Stored:       false,
			Duplicate:    duplicate,
			RelatedFacts: relatedFacts,
		}
		return mcp.NewToolResultStructured(result, formatStoreFactResult(result)), nil
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

	pointID := PointID(namespace, fact)
	if err := s.qdrant.Upsert(ctx, qdrant.Point{
		ID:      pointID,
		Vector:  vec,
		Payload: payload,
	}); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("store failed: %v", err)), nil
	}

	s.cache.Invalidate()

	result := StoreFactResult{
		Status:       "stored",
		Stored:       true,
		PointID:      pointID,
		RelatedFacts: relatedFacts,
	}
	return mcp.NewToolResultStructured(result, formatStoreFactResult(result)), nil
}

func (s *Server) recallFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query := strParam(args, "query")
	if err := validateBoundedString("query", query, maxQueryBytes, true); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validateCommonMetadata(args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	tags := tagsParam(args)
	namespace := strParam(args, "namespace")
	limit, err := intParam(args, "limit", 5)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if validationError := validatePositiveLimit("limit", limit); validationError != "" {
		return mcp.NewToolResultError(validationError), nil
	}

	cacheKey := fmt.Sprintf("%s|%s|%v|%d|%s", query, namespace, tags, limit, lifecycleCacheScope)
	if cached, ok := s.cache.Get(cacheKey); ok {
		if err := s.countRecalls(ctx, cached); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record recall failed: %v", err)), nil
		}
		s.cache.Set(cacheKey, cached)
		return mcp.NewToolResultText(formatFacts(cached)), nil
	}

	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	results, err := s.qdrant.Search(ctx, vec, lifecycleCandidateLimit(limit), currentLifecycleFilters(s.buildFilters(tags, namespace)), nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	var hits []map[string]interface{}
	for _, lifecyclePoint := range currentSearchPoints(results) {
		p := lifecyclePoint.point
		hit := map[string]interface{}{
			"_point_id":    p.ID,
			"text":         p.Payload["text"],
			"score":        p.Score,
			"tags":         p.Payload["tags"],
			"primary_tag":  p.Payload["primary_tag"],
			"namespace":    p.Payload["namespace"],
			"recall_count": p.Payload["recall_count"],
		}
		addLifecycleMetadata(hit, lifecyclePoint.view)
		hits = append(hits, hit)

		if len(hits) >= limit {
			break
		}
	}

	if err := s.countRecalls(ctx, hits); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("record recall failed: %v", err)), nil
	}
	s.cache.Set(cacheKey, hits)
	return mcp.NewToolResultText(formatFacts(hits)), nil
}

func (s *Server) updateFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	newFact := strParam(args, "new_fact")
	if err := validateBoundedString("new_fact", newFact, maxFactBytes, true); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validateCommonMetadata(args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if strParam(args, "point_id") == "" {
		if err := validateBoundedString("old_query", strParam(args, "old_query"), maxQueryBytes, true); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}

	old, targetError := s.mutationTarget(ctx, args, "old_query")
	if targetError != "" {
		return mcp.NewToolResultError(targetError), nil
	}
	oldText, _ := old.Payload["text"].(string)
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
		payload["namespace"] = NormalizeNamespace(ns)
	}
	namespace := NormalizeNamespace(stringFromPayload(payload["namespace"]))
	payload["namespace"] = namespace
	newID := PointID(namespace, newFact)
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
	if err := validateNamespace(strParam(args, "namespace")); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if strParam(args, "point_id") == "" {
		if err := validateBoundedString("query", strParam(args, "query"), maxQueryBytes, true); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	target, targetError := s.mutationTarget(ctx, args, "query")
	if targetError != "" {
		return mcp.NewToolResultError(targetError), nil
	}
	if err := s.qdrant.Delete(ctx, []string{target.ID}); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("delete failed: %v", err)), nil
	}

	s.cache.Invalidate()
	text, _ := target.Payload["text"].(string)
	return mcp.NewToolResultText(fmt.Sprintf("Deleted: %s (score %.2f)", text, target.Score)), nil
}

func (s *Server) forgetOld(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	days, err := intParam(args, "days", 90)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if days <= 0 {
		return mcp.NewToolResultError("days must be greater than zero"), nil
	}
	namespace := strParam(args, "namespace")
	if err := validateNamespace(namespace); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
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
	if len(factsRaw) > maxImportBytes {
		return mcp.NewToolResultError(fmt.Sprintf("facts must be at most %d bytes", maxImportBytes)), nil
	}

	// Parse JSON array of fact objects.
	var facts []map[string]interface{}
	if err := json.Unmarshal([]byte(factsRaw), &facts); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid JSON: %v", err)), nil
	}
	if len(facts) > maxImportFacts {
		return mcp.NewToolResultError(fmt.Sprintf("facts must contain at most %d entries", maxImportFacts)), nil
	}

	imported := 0
	skipped := 0
	for _, f := range facts {
		text, _ := f["text"].(string)
		if err := validateBoundedString("text", text, maxFactBytes, true); err != nil {
			skipped++
			continue
		}
		if err := validateNamespace(stringFromPayload(f["namespace"])); err != nil {
			skipped++
			continue
		}
		if err := validateTagsPayload(f["tags"]); err != nil {
			skipped++
			continue
		}
		if err := validateBoundedString("primary_tag", stringFromPayload(f["primary_tag"]), maxTagBytes, false); err != nil {
			skipped++
			continue
		}

		vec, err := s.embed.Embed(ctx, text)
		if err != nil {
			slog.Warn("import embed failed", "text", text, "error", err)
			skipped++
			continue
		}

		namespace := NormalizeNamespace(stringFromPayload(f["namespace"]))

		// Deduplication is namespace-scoped and lifecycle-aware, matching
		// store_fact semantics. Search failures preserve the existing fail-open
		// import behavior, while a saturated non-blocking window is inconclusive.
		dedupLimit := lifecycleCandidateLimit(relatedFactResultLimit)
		dedupLow := s.dedupThreshold
		existing, searchErr := s.qdrant.Search(ctx, vec, dedupLimit, s.buildFilters(nil, namespace), &dedupLow)
		if searchErr == nil {
			duplicate, _ := selectRelatedCandidates(existing, s.relatedFactLow, s.dedupThreshold, relatedFactResultLimit)
			if duplicate != nil || len(existing) == dedupLimit {
				skipped++
				continue
			}
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
			ID:      PointID(namespace, text),
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
	if err := validateBoundedString("query", query, maxQueryBytes, true); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	namespace := strParam(args, "namespace")
	if err := validateNamespace(namespace); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit, err := intParam(args, "limit", 5)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if validationError := validatePositiveLimit("limit", limit); validationError != "" {
		return mcp.NewToolResultError(validationError), nil
	}

	vec, err := s.embed.Embed(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("embedding failed: %v", err)), nil
	}

	low := s.relatedFactLow
	results, err := s.qdrant.Search(ctx, vec, lifecycleCandidateLimit(limit), s.buildFilters(nil, namespace), &low)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	_, relatedFacts := selectRelatedCandidates(results, s.relatedFactLow, s.dedupThreshold, limit)
	structured := FindRelatedResult{RelatedFacts: relatedFacts, Count: len(relatedFacts)}
	return mcp.NewToolResultStructured(structured, formatFindRelatedResult(structured)), nil
}

func (s *Server) listFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	if err := validateCommonMetadata(args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
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
		lifecycleSummary := formatLifecycleView(lifecycleView(p.ID, p.Payload))
		lines = append(lines, fmt.Sprintf("- [%s] %s%s ns:%s%s recalls:%d %s %s", createdAt, tagsList, primary, ns, perm, rc, lifecycleSummary, text))
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
	lifecycleStateCounts, legacyLifecycle, invalidLifecycle := lifecycleCounts(points)
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
	sb.WriteString("\nLifecycle states:\n")
	for _, line := range sortLifecycleCounts(lifecycleStateCounts) {
		sb.WriteString(line + "\n")
	}
	fmt.Fprintf(&sb, "  legacy (no lifecycle fields): %d\n", legacyLifecycle)
	fmt.Fprintf(&sb, "  invalid explicit metadata: %d\n", invalidLifecycle)

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
	if err := validateNamespace(namespace); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

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
	if err := validateNamespace(namespace); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

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

func (s *Server) getOperationalFacts(ctx context.Context, namespace string, topRecalled int) ([]lifecycleOperationalPoint, error) {
	filters := currentLifecycleFilters(s.buildFilters(nil, namespace))

	points, err := s.qdrant.ScrollAll(ctx, filters, false)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var permanent []lifecycleOperationalPoint
	var nonPermanent []lifecycleOperationalPoint

	for _, p := range points {
		view := lifecycleView(p.ID, p.Payload)
		if !lifecycle.IsCurrentTruth(view, isExpired(p.Payload)) {
			continue
		}
		lifecyclePoint := lifecycleOperationalPoint{point: p, view: view}
		if v, ok := p.Payload["permanent"].(bool); ok && v {
			permanent = append(permanent, lifecyclePoint)
			seen[p.ID] = true
		} else {
			nonPermanent = append(nonPermanent, lifecyclePoint)
		}
	}
	// Rank non-permanent facts by canonical authority, then recall count, and take top N.
	sort.Slice(nonPermanent, func(i, j int) bool {
		left := nonPermanent[i].view
		right := nonPermanent[j].view
		if left.Canonical != right.Canonical {
			return left.Canonical
		}
		ri, _ := nonPermanent[i].point.Payload["recall_count"].(float64)
		rj, _ := nonPermanent[j].point.Payload["recall_count"].(float64)
		if ri != rj {
			return ri > rj
		}
		return nonPermanent[i].point.ID < nonPermanent[j].point.ID
	})

	result := permanent
	added := 0
	for _, p := range nonPermanent {
		if added >= topRecalled {
			break
		}
		rc, _ := p.point.Payload["recall_count"].(float64)
		if rc == 0 {
			continue // never-recalled facts do not consume the bounded selection
		}
		if !seen[p.point.ID] {
			result = append(result, p)
			added++
		}
	}
	sortOperationalPoints(result)

	return result, nil
}

func (s *Server) getOperationalContext(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	namespace := strParam(args, "namespace")
	if err := validateNamespace(namespace); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	topRecalled, err := intParam(args, "top_recalled", 10)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if validationError := validatePositiveLimit("top_recalled", topRecalled); validationError != "" {
		return mcp.NewToolResultError(validationError), nil
	}

	points, err := s.getOperationalFacts(ctx, namespace, topRecalled)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("scroll failed: %v", err)), nil
	}

	if len(points) == 0 {
		return mcp.NewToolResultText("No operational context found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Operational Context (%d facts)\n\n", len(points))
	for _, lifecyclePoint := range points {
		p := lifecyclePoint.point
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
		lifecycleSummary := formatLifecycleView(lifecyclePoint.view)
		fmt.Fprintf(&sb, "- %s%s ns:%s%s recalls:%d %s %s\n", tagsList, primary, ns, perm, rc, lifecycleSummary, text)
	}
	return mcp.NewToolResultText(sb.String()), nil
}

// OperationalContextHandler returns an HTTP handler for GET /memory/operational.
// Returns operational facts as plain text, suitable for Claude Code hooks.
func (s *Server) OperationalContextHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		namespace := r.URL.Query().Get("namespace")
		if err := validateNamespace(namespace); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		points, err := s.getOperationalFacts(r.Context(), namespace, 10)
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
		for _, lifecyclePoint := range points {
			p := lifecyclePoint.point
			text, _ := p.Payload["text"].(string)
			ns, _ := p.Payload["namespace"].(string)
			lifecycleSummary := formatLifecycleView(lifecyclePoint.view)
			fmt.Fprintf(&sb, "- [%s] %s %s\n", ns, lifecycleSummary, text)
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
		lifecycleSummary := lifecycleSummaryFromHit(h)

		line := fmt.Sprintf("- [%.3f] %s%s ns:%s %s %s", h["score"], tagsList, primary, ns, lifecycleSummary, text)
		if rc := payloadInt(h["recall_count"]); rc > 0 {
			line = fmt.Sprintf("- [%.3f] %s%s ns:%s recalls:%d %s %s", h["score"], tagsList, primary, ns, rc, lifecycleSummary, text)
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
