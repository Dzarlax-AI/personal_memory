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

	// Static assets: /viz/assets/styles.css, /viz/assets/js/*.js
	if sub, err := fs.Sub(staticFS, "static/assets"); err == nil {
		r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
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

	points, err := h.qdrant.ScrollAll(r.Context(), nil, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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
	return map[string]interface{}{
		"id":           p.ID,
		"text":         p.Payload["text"],
		"namespace":    p.Payload["namespace"],
		"tags":         p.Payload["tags"],
		"created_at":   p.Payload["created_at"],
		"permanent":    p.Payload["permanent"],
		"recall_count": p.Payload["recall_count"],
	}
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
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
