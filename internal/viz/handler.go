package viz

import (
	"container/heap"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/go-chi/chi/v5"
)

const (
	defaultGraphMaxNodes = 1000
	hardGraphMaxNodes    = 5000
	hardGraphMaxEdges    = 5000
	defaultMaxPairs      = 500
	hardMaxPairs         = 5000
	graphScrollPageSize  = 100
	hardGraphScanPoints  = 50000
	maxTagsBodyBytes     = 64 << 10
	maxFactTags          = 50
	maxFactTagLength     = 100
	documentsCacheTTL    = 30 * time.Second
)

const vizComputationTimeout = 20 * time.Second

var errGraphScanLimit = errors.New("graph scan point limit exceeded")

type graphEdge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Similarity float64 `json:"similarity"`
}

type edgeHeap []graphEdge

func (h edgeHeap) Len() int { return len(h) }
func (h edgeHeap) Less(i, j int) bool {
	if h[i].Similarity != h[j].Similarity {
		return h[i].Similarity < h[j].Similarity
	}
	if h[i].From != h[j].From {
		return h[i].From > h[j].From
	}
	return h[i].To > h[j].To
}
func (h edgeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *edgeHeap) Push(x interface{}) { *h = append(*h, x.(graphEdge)) }
func (h *edgeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type duplicateCandidate struct {
	i, j       int
	similarity float64
}

type duplicateHeap []duplicateCandidate

func (h duplicateHeap) Len() int { return len(h) }
func (h duplicateHeap) Less(i, j int) bool {
	if h[i].similarity != h[j].similarity {
		return h[i].similarity < h[j].similarity
	}
	if h[i].i != h[j].i {
		return h[i].i > h[j].i
	}
	return h[i].j > h[j].j
}
func (h duplicateHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *duplicateHeap) Push(x interface{}) { *h = append(*h, x.(duplicateCandidate)) }
func (h *duplicateHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

//go:embed all:static
var staticFS embed.FS

// Order matters: this is how the view fragments are concatenated into the
// shell, which also determines the DOM order of the view containers.
var viewNames = []string{"overview", "duplicates", "forgotten", "timeline", "graph", "documents"}

type Handler struct {
	qdrant           *qdrant.Client
	defaultThreshold float64
	defaultMaxEdges  int

	docChunks *qdrant.Client
	docsDir   string

	// documentCache keeps the expensive, full-chunk document inventory bounded
	// to one scan per TTL in the usual case. documentRefresh coalesces concurrent
	// cache misses into that one scan. Reindexing is external to this handler, so
	// bounded staleness is safer than attempting to infer every index change.
	// Failed or canceled scans are never cached.
	documentCacheMu   sync.Mutex
	documentCache     *documentsResponse
	documentCacheTill time.Time
	documentsCacheTTL time.Duration
	documentRefresh   *documentsRefresh

	composedHTML []byte // shell.html with <!-- VIEWS --> expanded, built once at startup
}

type documentsRefresh struct {
	done chan struct{}
	err  error
}

func NewHandler(qc *qdrant.Client, defaultThreshold float64) *Handler {
	h := &Handler{
		qdrant:           qc,
		defaultThreshold: defaultThreshold,
		defaultMaxEdges:  500,
	}
	html, err := buildShellHTML()
	if err != nil {
		panic(fmt.Errorf("viz: build shell html: %w", err))
	}
	h.composedHTML = html
	return h
}

// WithDocumentRAG enables the Documents tab backed by the given chunks
// collection. docsDir is used to render paths relative to the root.
func (h *Handler) WithDocumentRAG(chunks *qdrant.Client, docsDir string) *Handler {
	h.docChunks = chunks
	h.docsDir = docsDir
	if h.documentsCacheTTL == 0 {
		h.documentsCacheTTL = documentsCacheTTL
	}
	return h
}

func (h *Handler) Router() chi.Router {
	r := chi.NewRouter()

	r.Get("/api/facts", h.apiFacts)
	r.Get("/api/facts/{id}", h.apiFactDetail)
	r.Get("/api/graph", h.apiGraph)
	r.Get("/api/duplicates", h.apiDuplicates)
	r.Get("/api/documents", h.apiDocuments)
	r.Get("/api/documents/status", h.apiDocumentsStatus)
	r.Patch("/api/facts/{id}/tags", h.apiUpdateFactTags)

	// Static assets: /viz/assets/styles.css, /viz/assets/js/*.js.
	// chi.Mount does NOT rewrite r.URL.Path — only its internal RoutePath — so
	// we can't strip "/assets" from the incoming URL (it still has the mount
	// prefix, e.g. "/viz/assets/styles.css"). Use chi.RouteContext to get the
	// full matched pattern and strip that instead.
	if sub, err := fs.Sub(staticFS, "static/assets"); err == nil {
		r.Get("/assets/*", func(w http.ResponseWriter, req *http.Request) {
			rctx := chi.RouteContext(req.Context())
			prefix := strings.TrimSuffix(rctx.RoutePattern(), "/*")
			http.StripPrefix(prefix, http.FileServer(http.FS(sub))).ServeHTTP(w, req)
		})
	}

	// Shell for the root and every recognised tab path
	// (/viz/, /viz/overview, /viz/documents, …).
	r.Get("/", h.serveIndex)
	r.Get("/{tab}", h.serveIndex)
	return r
}

func buildShellHTML() ([]byte, error) {
	shell, err := staticFS.ReadFile("static/shell.html")
	if err != nil {
		return nil, fmt.Errorf("read shell: %w", err)
	}
	var views strings.Builder
	for _, name := range viewNames {
		b, err := staticFS.ReadFile("static/views/" + name + ".html")
		if err != nil {
			return nil, fmt.Errorf("read view %s: %w", name, err)
		}
		views.Write(b)
		views.WriteString("\n")
	}
	return []byte(strings.Replace(string(shell), "<!-- VIEWS -->", views.String(), 1)), nil
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(h.composedHTML)
}

func (h *Handler) apiFacts(w http.ResponseWriter, r *http.Request) {
	points, err := h.qdrant.ScrollAll(r.Context(), nil, false)
	if err != nil {
		writeInternalError(w, "unable to load facts")
		return
	}

	nodes := make([]factSummary, 0, len(points))
	for _, p := range points {
		nodes = append(nodes, pointToSummary(p))
	}

	writeJSON(w, map[string]interface{}{"nodes": nodes})
}

// apiFactDetail is intentionally separate from the list endpoint: raw payload
// data is only returned after a user has selected one specific fact.
func (h *Handler) apiFactDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "missing point id", http.StatusBadRequest)
		return
	}
	point, found, err := h.qdrant.Get(r.Context(), id)
	if err != nil {
		writeInternalError(w, "unable to load fact detail")
		return
	}
	if !found {
		http.Error(w, "fact not found", http.StatusNotFound)
		return
	}
	writeJSON(w, pointToDetail(point))
}

func (h *Handler) apiGraph(w http.ResponseWriter, r *http.Request) {
	threshold, err := boundedFloatParam(r, "threshold", h.defaultThreshold, 0, 1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	maxEdges, err := boundedIntParam(r, "max_edges", h.defaultMaxEdges, 1, hardGraphMaxEdges)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	maxNodes, err := boundedIntParam(r, "max_nodes", defaultGraphMaxNodes, 1, hardGraphMaxNodes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	tag := r.URL.Query().Get("tag")
	primaryTag := r.URL.Query().Get("primary_tag")
	textState := r.URL.Query().Get("text")

	ctx, cancel := context.WithTimeout(r.Context(), vizComputationTimeout)
	defer cancel()
	points, tooMany, err := h.scrollGraphPoints(ctx, maxNodes, namespace, tag, primaryTag, textState)
	if err != nil {
		writeGraphComputationError(w, err)
		return
	}
	if tooMany {
		http.Error(w, fmt.Sprintf("filtered dataset has more than %d nodes; narrow the filters or raise max_nodes up to %d", maxNodes, hardGraphMaxNodes), http.StatusUnprocessableEntity)
		return
	}

	nodes := make([]factSummary, 0, len(points))
	for _, p := range points {
		nodes = append(nodes, pointToSummary(p))
	}

	edgeResults, err := strongestGraphEdges(ctx, points, threshold, maxEdges)
	if err != nil {
		writeGraphComputationError(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"nodes": nodes,
		"edges": edgeResults,
	})
}

// scrollGraphPoints pages through Qdrant and retains at most maxNodes+1 local
// filter matches. The extra point is only used to report that the requested
// computation would exceed its explicit bound.
func (h *Handler) scrollGraphPoints(ctx context.Context, maxNodes int, namespace, tag, primaryTag, textState string) ([]qdrant.ScrollPoint, bool, error) {
	return h.scrollGraphPointsWithLimit(ctx, maxNodes, hardGraphScanPoints, namespace, tag, primaryTag, textState)
}

func (h *Handler) scrollGraphPointsWithLimit(ctx context.Context, maxNodes, maxScanned int, namespace, tag, primaryTag, textState string) ([]qdrant.ScrollPoint, bool, error) {
	points := make([]qdrant.ScrollPoint, 0, maxNodes+1)
	filters := qdrantGraphFilters(namespace, tag, primaryTag)
	scanned := 0
	var offset interface{}
	for {
		page, err := h.qdrant.Scroll(ctx, graphScrollPageSize, offset, filters, true)
		if err != nil {
			return nil, false, err
		}
		scanned += len(page.Points)
		if scanned > maxScanned {
			return nil, false, fmt.Errorf("%w: scanned more than %d points; narrow the filters", errGraphScanLimit, maxScanned)
		}
		for _, point := range page.Points {
			if !graphPointMatches(point, namespace, tag, primaryTag, textState) {
				continue
			}
			points = append(points, point)
			if len(points) > maxNodes {
				return points[:maxNodes], true, nil
			}
		}
		if page.RawOffset == nil {
			return points, false, nil
		}
		offset = page.RawOffset
	}
}

func qdrantGraphFilters(namespace, tag, primaryTag string) map[string]interface{} {
	must := make([]map[string]interface{}, 0, 3)
	if namespace != "" && namespace != "__missing__" {
		must = append(must, map[string]interface{}{"key": "namespace", "match": map[string]interface{}{"value": namespace}})
	}
	if tag != "" {
		must = append(must, map[string]interface{}{"key": "tags", "match": map[string]interface{}{"value": tag}})
	}
	if primaryTag != "" {
		must = append(must, map[string]interface{}{"key": "primary_tag", "match": map[string]interface{}{"value": primaryTag}})
	}
	if len(must) == 0 {
		return nil
	}
	return map[string]interface{}{"must": must}
}

func writeGraphComputationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGraphScanLimit):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "graph computation timed out or was canceled", http.StatusRequestTimeout)
	default:
		writeInternalError(w, "unable to compute graph")
	}
}

func strongestGraphEdges(ctx context.Context, points []qdrant.ScrollPoint, threshold float64, maxEdges int) ([]graphEdge, error) {
	edges := &edgeHeap{}
	heap.Init(edges)
	for i := 0; i < len(points); i++ {
		for j := i + 1; j < len(points); j++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sim := cosineSimilarity(points[i].Vector, points[j].Vector)
			if sim >= threshold {
				keepStrongestEdge(edges, graphEdge{From: points[i].ID, To: points[j].ID, Similarity: sim}, maxEdges)
			}
		}
	}
	results := append([]graphEdge(nil), (*edges)...)
	sort.Slice(results, func(i, j int) bool { return strongerEdge(results[i], results[j]) })
	return results, nil
}

func filterGraphPoints(points []qdrant.ScrollPoint, namespace, tag, primaryTag, textState string) []qdrant.ScrollPoint {
	if namespace == "" && tag == "" && primaryTag == "" && textState == "" {
		return points
	}

	filtered := make([]qdrant.ScrollPoint, 0, len(points))
	for _, p := range points {
		if graphPointMatches(p, namespace, tag, primaryTag, textState) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func graphPointMatches(p qdrant.ScrollPoint, namespace, tag, primaryTag, textState string) bool {
	if namespace != "" {
		if namespace == "__missing__" {
			if !payloadNamespaceMissing(p.Payload["namespace"]) {
				return false
			}
		} else if ns, _ := p.Payload["namespace"].(string); ns != namespace {
			return false
		}
	}
	if tag != "" && !payloadHasTag(p.Payload["tags"], tag) {
		return false
	}
	if primaryTag != "" {
		if primary, _ := p.Payload["primary_tag"].(string); primary != primaryTag {
			return false
		}
	}
	text, _ := payloadText(p.Payload)
	if textState == "missing" && text != "" {
		return false
	}
	if textState == "present" && text == "" {
		return false
	}
	return true
}

func payloadNamespaceMissing(raw interface{}) bool {
	ns, ok := raw.(string)
	return raw == nil || !ok || ns == "" || ns == "null"
}

func payloadHasTag(raw interface{}, tag string) bool {
	switch tags := raw.(type) {
	case []string:
		for _, t := range tags {
			if t == tag {
				return true
			}
		}
	case []interface{}:
		for _, v := range tags {
			if t, ok := v.(string); ok && t == tag {
				return true
			}
		}
	}
	return false
}

func (h *Handler) apiDuplicates(w http.ResponseWriter, r *http.Request) {
	threshold, err := boundedFloatParam(r, "threshold", 0.90, 0, 1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	maxNodes, err := boundedIntParam(r, "max_nodes", defaultGraphMaxNodes, 1, hardGraphMaxNodes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	maxPairs, err := boundedIntParam(r, "max_pairs", defaultMaxPairs, 1, hardMaxPairs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), vizComputationTimeout)
	defer cancel()
	points, tooMany, err := h.scrollGraphPoints(ctx, maxNodes, "", "", "", "")
	if err != nil {
		writeGraphComputationError(w, err)
		return
	}
	if tooMany {
		http.Error(w, fmt.Sprintf("dataset has more than %d nodes; raise max_nodes up to %d to run a larger bounded scan", maxNodes, hardGraphMaxNodes), http.StatusUnprocessableEntity)
		return
	}

	type dupPair struct {
		A          factSummary `json:"a"`
		B          factSummary `json:"b"`
		Similarity float64     `json:"similarity"`
	}

	candidates, err := strongestDuplicates(ctx, points, threshold, maxPairs)
	if err != nil {
		writeGraphComputationError(w, err)
		return
	}
	pairs := make([]dupPair, len(candidates))
	for i, candidate := range candidates {
		pairs[i] = dupPair{A: pointToSummary(points[candidate.i]), B: pointToSummary(points[candidate.j]), Similarity: candidate.similarity}
	}

	writeJSON(w, map[string]interface{}{"pairs": pairs})
}

func strongestDuplicates(ctx context.Context, points []qdrant.ScrollPoint, threshold float64, maxPairs int) ([]duplicateCandidate, error) {
	candidates := &duplicateHeap{}
	heap.Init(candidates)
	for i := 0; i < len(points); i++ {
		for j := i + 1; j < len(points); j++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sim := cosineSimilarity(points[i].Vector, points[j].Vector)
			if sim >= threshold {
				keepStrongestDuplicate(candidates, duplicateCandidate{i: i, j: j, similarity: sim}, maxPairs)
			}
		}
	}
	results := append([]duplicateCandidate(nil), (*candidates)...)
	sort.Slice(results, func(i, j int) bool { return strongerDuplicate(results[i], results[j]) })
	return results, nil
}

// factSummary is the only fact shape returned by collection/list endpoints.
// Keep it intentionally small: lifecycle is normalized metadata, while raw
// payload and unrelated diagnostics stay limited to explicit detail requests.
type factSummary struct {
	ID          string         `json:"id"`
	Text        string         `json:"text"`
	TextMissing bool           `json:"text_missing"`
	Namespace   string         `json:"namespace"`
	Tags        []string       `json:"tags"`
	PrimaryTag  string         `json:"primary_tag"`
	CreatedAt   string         `json:"created_at"`
	Permanent   bool           `json:"permanent"`
	RecallCount int            `json:"recall_count"`
	Lifecycle   lifecycle.View `json:"lifecycle"`
}

// factDetail extends the privacy-safe summary with the complete payload for
// one explicitly selected fact. It must only be used by apiFactDetail.
type factDetail struct {
	factSummary
	TextSource  string                 `json:"text_source"`
	PayloadKeys []string               `json:"payload_keys"`
	Payload     map[string]interface{} `json:"payload"`
}

func pointToSummary(p qdrant.ScrollPoint) factSummary {
	text, _ := payloadText(p.Payload)
	lifecycleView, _ := lifecycle.Parse(p.Payload, p.ID)
	return factSummary{
		ID:          p.ID,
		Text:        text,
		TextMissing: text == "",
		Namespace:   payloadStringValue(p.Payload, "namespace"),
		Tags:        payloadStringSlice(p.Payload["tags"]),
		PrimaryTag:  payloadStringValue(p.Payload, "primary_tag"),
		CreatedAt:   payloadStringValue(p.Payload, "created_at", "created", "timestamp", "date"),
		Permanent:   payloadBool(p.Payload["permanent"]),
		RecallCount: payloadInt(p.Payload["recall_count"]),
		Lifecycle:   lifecycleView,
	}
}

func pointToDetail(p qdrant.Point) factDetail {
	summary := pointToSummary(qdrant.ScrollPoint{ID: p.ID, Payload: p.Payload})
	_, source := payloadText(p.Payload)
	return factDetail{
		factSummary: summary,
		TextSource:  source,
		PayloadKeys: payloadKeys(p.Payload),
		Payload:     p.Payload,
	}
}

func payloadStringSlice(raw interface{}) []string {
	var values []string
	switch tags := raw.(type) {
	case []string:
		values = append(values, tags...)
	case []interface{}:
		for _, value := range tags {
			if tag, ok := value.(string); ok {
				values = append(values, tag)
			}
		}
	}
	return values
}

func payloadBool(raw interface{}) bool {
	value, _ := raw.(bool)
	return value
}

func payloadInt(raw interface{}) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	default:
		return 0
	}
}

func payloadText(payload map[string]interface{}) (string, string) {
	if text, key := payloadString(payload, "text", "fact", "content", "memory", "body", "note", "value"); text != "" {
		return text, key
	}
	if text, path := deepPayloadText(payload, 0, ""); text != "" {
		return text, path
	}
	return "", ""
}

func payloadString(payload map[string]interface{}, keys ...string) (string, string) {
	for _, key := range keys {
		v, ok := payload[key]
		if !ok || v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s, key
		}
	}
	return "", ""
}

func payloadStringValue(payload map[string]interface{}, keys ...string) string {
	value, _ := payloadString(payload, keys...)
	return value
}

func deepPayloadText(v interface{}, depth int, path string) (string, string) {
	if depth > 5 {
		return "", ""
	}
	switch value := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if isDiagnosticTextKey(key) {
				continue
			}
			nextPath := key
			if path != "" {
				nextPath = path + "." + key
			}
			if isTextLikeKey(key) {
				if s, ok := value[key].(string); ok && strings.TrimSpace(s) != "" {
					return s, nextPath
				}
			}
		}
		for _, key := range keys {
			nextPath := key
			if path != "" {
				nextPath = path + "." + key
			}
			if text, source := deepPayloadText(value[key], depth+1, nextPath); text != "" {
				return text, source
			}
		}
	case []interface{}:
		for i, item := range value {
			nextPath := fmt.Sprintf("%s[%d]", path, i)
			if text, source := deepPayloadText(item, depth+1, nextPath); text != "" {
				return text, source
			}
		}
	}
	return "", ""
}

func isDiagnosticTextKey(key string) bool {
	k := strings.ToLower(key)
	return k == "nearest_text" || k == "recovered_text" || strings.HasPrefix(k, "nearest_") || strings.HasPrefix(k, "recovery_")
}

func isTextLikeKey(key string) bool {
	k := strings.ToLower(key)
	for _, candidate := range []string{"text", "fact", "content", "memory", "body", "note", "value", "message", "description", "summary", "title"} {
		if k == candidate || strings.HasSuffix(k, "_"+candidate) {
			return true
		}
	}
	return false
}

func payloadKeys(payload map[string]interface{}) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeInternalError(w http.ResponseWriter, message string) {
	http.Error(w, message, http.StatusInternalServerError)
}

func (h *Handler) apiUpdateFactTags(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Viz-Action") != "update-tags" {
		http.Error(w, "missing X-Viz-Action header", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "missing point id", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTagsBodyBytes)
	var req struct {
		Tags       []string `json:"tags"`
		PrimaryTag string   `json:"primary_tag"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		http.Error(w, "request body must contain a single JSON object", http.StatusBadRequest)
		return
	} else if !errors.Is(err, io.EOF) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tags, primaryTag, err := normalizeFactTags(req.Tags, req.PrimaryTag)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.qdrant.SetPayload(r.Context(), id, map[string]interface{}{"tags": tags, "primary_tag": primaryTag}); err != nil {
		writeInternalError(w, "unable to update fact tags")
		return
	}
	writeJSON(w, map[string]interface{}{"id": id, "tags": tags, "primary_tag": primaryTag})
}

func normalizeFactTags(tags []string, primary string) ([]string, string, error) {
	if len(tags) > maxFactTags {
		return nil, "", fmt.Errorf("tags must contain at most %d entries", maxFactTags)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		t := strings.TrimSpace(tag)
		if t == "" {
			return nil, "", fmt.Errorf("tags must not contain blank values")
		}
		if len([]rune(t)) > maxFactTagLength {
			return nil, "", fmt.Errorf("tags must be at most %d characters", maxFactTagLength)
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	primary = strings.TrimSpace(primary)
	if primary != "" {
		if len([]rune(primary)) > maxFactTagLength {
			return nil, "", fmt.Errorf("primary_tag must be at most %d characters", maxFactTagLength)
		}
		if _, ok := seen[primary]; !ok {
			if len(out) >= maxFactTags {
				return nil, "", fmt.Errorf("adding primary_tag would exceed the %d tag limit", maxFactTags)
			}
			out = append(out, primary)
			sort.Strings(out)
		}
		return out, primary, nil
	}
	if len(out) == 1 {
		return out, out[0], nil
	}
	return out, "", nil
}

func boundedFloatParam(r *http.Request, name string, def, min, max float64) (float64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		if def < min || def > max {
			return 0, fmt.Errorf("default %s must be between %g and %g", name, min, max)
		}
		return def, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
		return 0, fmt.Errorf("%s must be a number between %g and %g", name, min, max)
	}
	return value, nil
}

func boundedIntParam(r *http.Request, name string, def, min, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, min, max)
	}
	return value, nil
}

func keepStrongestEdge(edges *edgeHeap, candidate graphEdge, limit int) {
	if edges.Len() < limit {
		heap.Push(edges, candidate)
		return
	}
	if strongerEdge(candidate, (*edges)[0]) {
		heap.Pop(edges)
		heap.Push(edges, candidate)
	}
}

func strongerEdge(a, b graphEdge) bool {
	if a.Similarity != b.Similarity {
		return a.Similarity > b.Similarity
	}
	if a.From != b.From {
		return a.From < b.From
	}
	return a.To < b.To
}

func keepStrongestDuplicate(pairs *duplicateHeap, candidate duplicateCandidate, limit int) {
	if pairs.Len() < limit {
		heap.Push(pairs, candidate)
		return
	}
	if strongerDuplicate(candidate, (*pairs)[0]) {
		heap.Pop(pairs)
		heap.Push(pairs, candidate)
	}
}

func strongerDuplicate(a, b duplicateCandidate) bool {
	if a.similarity != b.similarity {
		return a.similarity > b.similarity
	}
	if a.i != b.i {
		return a.i < b.i
	}
	return a.j < b.j
}

// --- Documents (RAG) ---

type fileInfo struct {
	RelPath      string `json:"relative_path"`
	Chunks       int    `json:"chunks"`
	LastIndexed  string `json:"last_indexed"`
	FirstHeading string `json:"first_heading,omitempty"`
}

type folderInfo struct {
	RelPath     string     `json:"relative_path"`
	FileCount   int        `json:"file_count"`
	ChunkCount  int        `json:"chunk_count"`
	LastIndexed string     `json:"last_indexed"`
	Files       []fileInfo `json:"files"`
}

type documentsResponse struct {
	Stats struct {
		TotalFiles   int    `json:"total_files"`
		TotalChunks  int    `json:"total_chunks"`
		TotalFolders int    `json:"total_folders"`
		LastIndexed  string `json:"last_indexed"`
	} `json:"stats"`
	Folders        []folderInfo `json:"folders"`
	CacheExpiresAt string       `json:"cache_expires_at"`
}

type documentsStatusResponse struct {
	Enabled      bool   `json:"enabled"`
	Cached       bool   `json:"cached"`
	TotalFiles   int    `json:"total_files,omitempty"`
	TotalChunks  uint64 `json:"total_chunks"`
	TotalFolders int    `json:"total_folders,omitempty"`
	LastIndexed  string `json:"last_indexed,omitempty"`
}

func (h *Handler) apiDocuments(w http.ResponseWriter, r *http.Request) {
	if h.docChunks == nil {
		http.Error(w, "RAG not enabled", http.StatusNotFound)
		return
	}
	if err := r.Context().Err(); err != nil {
		writeDocumentsError(w, err)
		return
	}
	// refresh=1 explicitly bypasses a fresh inventory cache. Forced refreshes
	// still join any scan already in flight and replace the cache only after a
	// successful, non-canceled scan.
	forceRefresh := r.URL.Query().Get("refresh") == "1"
	if cached, ok := h.cachedDocuments(); ok && !forceRefresh {
		writeJSON(w, cached)
		return
	}
	refresh, owner := h.acquireDocumentsRefresh()
	if !owner {
		select {
		case <-refresh.done:
			if refresh.err != nil {
				writeDocumentsError(w, refresh.err)
				return
			}
			if cached, ok := h.cachedDocuments(); ok {
				writeJSON(w, cached)
				return
			}
			writeInternalError(w, "unable to load documents")
			return
		case <-r.Context().Done():
			writeDocumentsError(w, r.Context().Err())
			return
		}
	}

	// A completed refresh can race with the request becoming the owner. Check
	// again before doing any Qdrant work so an already fresh inventory wins.
	if cached, ok := h.cachedDocuments(); ok && !forceRefresh {
		// The cached response already carries its original expiry. Wake waiters
		// without republishing it or extending the TTL.
		h.finishDocumentsRefresh(refresh, nil, nil)
		writeJSON(w, cached)
		return
	}

	// The inventory belongs to the shared refresh, not to whichever request won
	// ownership. Keep request values for tracing, but detach cancellation so one
	// disconnected owner cannot poison every coalesced waiter. Each caller still
	// waits using its own request context below, while the scan remains bounded.
	scanCtx, cancelScan := context.WithTimeout(context.WithoutCancel(r.Context()), vizComputationTimeout)
	go func() {
		defer cancelScan()
		resp, err := h.buildDocumentsResponse(scanCtx)
		h.finishDocumentsRefresh(refresh, resp, err)
	}()

	select {
	case <-refresh.done:
		if refresh.err != nil {
			writeDocumentsError(w, refresh.err)
			return
		}
		if cached, ok := h.cachedDocuments(); ok {
			writeJSON(w, cached)
			return
		}
		writeInternalError(w, "unable to load documents")
	case <-r.Context().Done():
		writeDocumentsError(w, r.Context().Err())
	}
}

func (h *Handler) buildDocumentsResponse(ctx context.Context) (*documentsResponse, error) {
	fields := []string{"file_path", "folder_path", "chunk_index", "heading", "indexed_at"}
	points, err := h.docChunks.ScrollAllWithPayload(ctx, nil, fields, false)
	if err != nil {
		return nil, err
	}

	fileMap := map[string]*fileInfo{}
	for i, p := range points {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		fp, _ := p.Payload["file_path"].(string)
		if fp == "" {
			continue
		}
		fi, ok := fileMap[fp]
		if !ok {
			fi = &fileInfo{RelPath: h.relToDocs(fp)}
			fileMap[fp] = fi
		}
		fi.Chunks++
		if idx, ok := p.Payload["chunk_index"].(float64); ok && int(idx) == 0 {
			if heading, ok := p.Payload["heading"].(string); ok {
				fi.FirstHeading = heading
			}
		}
		if ts, ok := p.Payload["indexed_at"].(string); ok && ts > fi.LastIndexed {
			fi.LastIndexed = ts
		}
	}

	folderMap := map[string]*folderInfo{}
	i := 0
	for filePath, fi := range fileMap {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		i++
		folder := filepath.Dir(filePath)
		fo, ok := folderMap[folder]
		if !ok {
			fo = &folderInfo{RelPath: h.relToDocs(folder)}
			folderMap[folder] = fo
		}
		fo.FileCount++
		fo.ChunkCount += fi.Chunks
		if fi.LastIndexed > fo.LastIndexed {
			fo.LastIndexed = fi.LastIndexed
		}
		fo.Files = append(fo.Files, *fi)
	}

	folders := make([]folderInfo, 0, len(folderMap))
	var latest string
	for _, f := range folderMap {
		sort.Slice(f.Files, func(i, j int) bool { return f.Files[i].RelPath < f.Files[j].RelPath })
		folders = append(folders, *f)
		if f.LastIndexed > latest {
			latest = f.LastIndexed
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].RelPath < folders[j].RelPath })

	resp := documentsResponse{
		Folders: folders,
	}
	resp.Stats.TotalFiles = len(fileMap)
	resp.Stats.TotalChunks = len(points)
	resp.Stats.TotalFolders = len(folderMap)
	resp.Stats.LastIndexed = latest

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &resp, nil
}

func writeDocumentsError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "document inventory timed out or was canceled", http.StatusRequestTimeout)
		return
	}
	writeInternalError(w, "unable to load documents")
}

// acquireDocumentsRefresh returns exactly one owner for an expired cache. All
// other callers receive the same refresh and can wait with their own context.
func (h *Handler) acquireDocumentsRefresh() (*documentsRefresh, bool) {
	h.documentCacheMu.Lock()
	defer h.documentCacheMu.Unlock()
	if h.documentRefresh != nil {
		return h.documentRefresh, false
	}
	refresh := &documentsRefresh{done: make(chan struct{})}
	h.documentRefresh = refresh
	return refresh, true
}

func (h *Handler) finishDocumentsRefresh(refresh *documentsRefresh, resp *documentsResponse, err error) *documentsResponse {
	h.documentCacheMu.Lock()
	defer h.documentCacheMu.Unlock()
	var published *documentsResponse
	if err == nil && resp != nil {
		ttl := h.documentsCacheTTL
		if ttl <= 0 {
			ttl = documentsCacheTTL
		}
		// Keep the native deadline (including its monotonic clock reading) for
		// internal expiry checks. UTC conversion is only for serialization.
		deadline := time.Now().Add(ttl)
		// Publish a shallow copy and never mutate it afterward. The response's
		// nested slices are fully built before this point and remain immutable.
		copy := *resp
		copy.CacheExpiresAt = deadline.UTC().Format(time.RFC3339Nano)
		published = &copy
		h.documentCache = published
		h.documentCacheTill = deadline
	}
	refresh.err = err
	close(refresh.done)
	if h.documentRefresh == refresh {
		h.documentRefresh = nil
	}
	return published
}

// apiDocumentsStatus never walks all chunks. It uses Qdrant's count endpoint
// for the current chunk count and adds richer stats only when the inventory
// cache is fresh.
func (h *Handler) apiDocumentsStatus(w http.ResponseWriter, r *http.Request) {
	if h.docChunks == nil {
		writeJSON(w, documentsStatusResponse{Enabled: false})
		return
	}
	count, err := h.docChunks.ExactCount(r.Context())
	if err != nil {
		writeInternalError(w, "unable to load document status")
		return
	}
	resp := documentsStatusResponse{Enabled: true, TotalChunks: count}
	if cached, ok := h.cachedDocuments(); ok {
		resp.Cached = true
		resp.TotalFiles = cached.Stats.TotalFiles
		resp.TotalFolders = cached.Stats.TotalFolders
		resp.LastIndexed = cached.Stats.LastIndexed
	}
	writeJSON(w, resp)
}

func (h *Handler) cachedDocuments() (*documentsResponse, bool) {
	h.documentCacheMu.Lock()
	defer h.documentCacheMu.Unlock()
	if h.documentCache == nil || time.Now().After(h.documentCacheTill) {
		return nil, false
	}
	return h.documentCache, true
}

func (h *Handler) relToDocs(path string) string {
	if path == "" {
		return ""
	}
	if isCrossPlatformAbsolute(path) {
		// Only native absolute paths can be safely related by filepath.Rel.
		// Foreign-style absolute paths (for example C:\\... on Unix) are
		// withheld even if their string prefix resembles the configured root.
		if h.docsDir == "" || !filepath.IsAbs(path) || !filepath.IsAbs(h.docsDir) {
			return ""
		}
		rel, err := filepath.Rel(h.docsDir, path)
		if err != nil || !safeRelativePath(filepath.ToSlash(rel)) {
			return ""
		}
		return filepath.ToSlash(rel)
	}

	// Drive-relative paths such as C:notes.txt are not absolute, but still carry
	// host-specific location data and are not safe dashboard paths.
	if hasWindowsDrivePrefix(path) {
		return ""
	}
	clean := pathpkg.Clean(strings.ReplaceAll(path, "\\", "/"))
	if !safeRelativePath(clean) {
		return ""
	}
	return clean
}

func isCrossPlatformAbsolute(path string) bool {
	return strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) ||
		(hasWindowsDrivePrefix(path) && len(path) >= 3 && (path[2] == '\\' || path[2] == '/'))
}

func hasWindowsDrivePrefix(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	drive := path[0]
	return (drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')
}

func safeRelativePath(path string) bool {
	return path != ".." && !strings.HasPrefix(path, "../") && !isCrossPlatformAbsolute(path)
}
