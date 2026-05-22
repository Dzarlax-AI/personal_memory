package viz

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/go-chi/chi/v5"
)

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

	composedHTML []byte // shell.html with <!-- VIEWS --> expanded, built once at startup
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
	return h
}

func (h *Handler) Router() chi.Router {
	r := chi.NewRouter()

	r.Get("/api/facts", h.apiFacts)
	r.Get("/api/graph", h.apiGraph)
	r.Get("/api/duplicates", h.apiDuplicates)
	r.Get("/api/documents", h.apiDocuments)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	nodes := make([]map[string]interface{}, 0, len(points))
	for _, p := range points {
		nodes = append(nodes, pointToNode(p))
	}

	writeJSON(w, map[string]interface{}{"nodes": nodes})
}

func (h *Handler) apiGraph(w http.ResponseWriter, r *http.Request) {
	threshold := h.defaultThreshold
	if v := r.URL.Query().Get("threshold"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}
	maxEdges := h.defaultMaxEdges
	if v := r.URL.Query().Get("max_edges"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxEdges = n
		}
	}
	namespace := r.URL.Query().Get("namespace")
	tag := r.URL.Query().Get("tag")
	primaryTag := r.URL.Query().Get("primary_tag")
	textState := r.URL.Query().Get("text")

	points, err := h.qdrant.ScrollAll(r.Context(), nil, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	points = filterGraphPoints(points, namespace, tag, primaryTag, textState)

	nodes := make([]map[string]interface{}, 0, len(points))
	for _, p := range points {
		nodes = append(nodes, pointToNode(p))
	}

	type edge struct {
		From       string  `json:"from"`
		To         string  `json:"to"`
		Similarity float64 `json:"similarity"`
	}

	var edges []edge
	for i := 0; i < len(points); i++ {
		for j := i + 1; j < len(points); j++ {
			sim := cosineSimilarity(points[i].Vector, points[j].Vector)
			if sim >= threshold {
				edges = append(edges, edge{
					From:       points[i].ID,
					To:         points[j].ID,
					Similarity: sim,
				})
			}
		}
	}

	if len(edges) > maxEdges {
		sort.Slice(edges, func(i, j int) bool {
			return edges[i].Similarity > edges[j].Similarity
		})
		edges = edges[:maxEdges]
	}

	writeJSON(w, map[string]interface{}{
		"nodes": nodes,
		"edges": edges,
	})
}

func filterGraphPoints(points []qdrant.ScrollPoint, namespace, tag, primaryTag, textState string) []qdrant.ScrollPoint {
	if namespace == "" && tag == "" && primaryTag == "" && textState == "" {
		return points
	}

	filtered := make([]qdrant.ScrollPoint, 0, len(points))
	for _, p := range points {
		if namespace != "" {
			if namespace == "__missing__" {
				if !payloadNamespaceMissing(p.Payload["namespace"]) {
					continue
				}
			} else if ns, _ := p.Payload["namespace"].(string); ns != namespace {
				continue
			}
		}
		if tag != "" && !payloadHasTag(p.Payload["tags"], tag) {
			continue
		}
		if primaryTag != "" {
			if primary, _ := p.Payload["primary_tag"].(string); primary != primaryTag {
				continue
			}
		}
		text, _ := payloadText(p.Payload)
		if textState == "missing" && text != "" {
			continue
		}
		if textState == "present" && text == "" {
			continue
		}
		filtered = append(filtered, p)
	}
	return filtered
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
	threshold := 0.90
	if v := r.URL.Query().Get("threshold"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}

	points, err := h.qdrant.ScrollAll(r.Context(), nil, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type dupPair struct {
		A          map[string]interface{} `json:"a"`
		B          map[string]interface{} `json:"b"`
		Similarity float64                `json:"similarity"`
	}

	var pairs []dupPair
	for i := 0; i < len(points); i++ {
		for j := i + 1; j < len(points); j++ {
			sim := cosineSimilarity(points[i].Vector, points[j].Vector)
			if sim >= threshold {
				pairs = append(pairs, dupPair{
					A:          pointToNode(points[i]),
					B:          pointToNode(points[j]),
					Similarity: sim,
				})
			}
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Similarity > pairs[j].Similarity
	})

	writeJSON(w, map[string]interface{}{"pairs": pairs})
}

func pointToNode(p qdrant.ScrollPoint) map[string]interface{} {
	text, source := payloadText(p.Payload)
	return map[string]interface{}{
		"id":           p.ID,
		"text":         text,
		"text_source":  source,
		"text_missing": text == "",
		"payload_keys": payloadKeys(p.Payload),
		"payload":      p.Payload,
		"namespace":    p.Payload["namespace"],
		"tags":         p.Payload["tags"],
		"primary_tag":  p.Payload["primary_tag"],
		"created_at":   payloadStringValue(p.Payload, "created_at", "created", "timestamp", "date"),
		"permanent":    p.Payload["permanent"],
		"recall_count": p.Payload["recall_count"],
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
	var req struct {
		Tags       []string `json:"tags"`
		PrimaryTag string   `json:"primary_tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tags, primaryTag := normalizeFactTags(req.Tags, req.PrimaryTag)
	if err := h.qdrant.SetPayload(r.Context(), id, map[string]interface{}{"tags": tags, "primary_tag": primaryTag}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"id": id, "tags": tags, "primary_tag": primaryTag})
}

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
	sort.Strings(out)
	primary = strings.TrimSpace(primary)
	if primary != "" {
		if _, ok := seen[primary]; !ok {
			out = append(out, primary)
			sort.Strings(out)
		}
		return out, primary
	}
	if len(out) == 1 {
		return out, out[0]
	}
	return out, ""
}

// --- Documents (RAG) ---

type fileInfo struct {
	Path         string `json:"path"`
	RelPath      string `json:"relative_path"`
	Chunks       int    `json:"chunks"`
	LastIndexed  string `json:"last_indexed"`
	FirstHeading string `json:"first_heading,omitempty"`
}

type folderInfo struct {
	Path        string     `json:"path"`
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
	DocumentsDir string       `json:"documents_dir"`
	Folders      []folderInfo `json:"folders"`
}

func (h *Handler) apiDocuments(w http.ResponseWriter, r *http.Request) {
	if h.docChunks == nil {
		http.Error(w, "RAG not enabled", http.StatusNotFound)
		return
	}

	fields := []string{"file_path", "folder_path", "chunk_index", "heading", "indexed_at"}
	points, err := h.docChunks.ScrollAllWithPayload(r.Context(), nil, fields, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fileMap := map[string]*fileInfo{}
	for _, p := range points {
		fp, _ := p.Payload["file_path"].(string)
		if fp == "" {
			continue
		}
		fi, ok := fileMap[fp]
		if !ok {
			fi = &fileInfo{Path: fp, RelPath: h.relToDocs(fp)}
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
	for _, fi := range fileMap {
		folder := filepath.Dir(fi.Path)
		fo, ok := folderMap[folder]
		if !ok {
			fo = &folderInfo{Path: folder, RelPath: h.relToDocs(folder)}
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
		sort.Slice(f.Files, func(i, j int) bool { return f.Files[i].Path < f.Files[j].Path })
		folders = append(folders, *f)
		if f.LastIndexed > latest {
			latest = f.LastIndexed
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Path < folders[j].Path })

	resp := documentsResponse{
		DocumentsDir: h.docsDir,
		Folders:      folders,
	}
	resp.Stats.TotalFiles = len(fileMap)
	resp.Stats.TotalChunks = len(points)
	resp.Stats.TotalFolders = len(folderMap)
	resp.Stats.LastIndexed = latest

	writeJSON(w, resp)
}

func (h *Handler) relToDocs(abs string) string {
	if h.docsDir == "" || abs == "" {
		return abs
	}
	r, err := filepath.Rel(h.docsDir, abs)
	if err != nil {
		return abs
	}
	return r
}
