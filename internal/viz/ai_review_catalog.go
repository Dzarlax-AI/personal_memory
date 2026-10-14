package viz

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/go-chi/chi/v5"
)

const maxCatalogActionBody = 16 << 10

var operationIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (h *Handler) catalogActionsAvailable() bool {
	return h.reviewWritable() && h.aiReviewConfig.CatalogDir != ""
}

func (h *Handler) apiAIReviewVersions(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "project_id")
	unknownQuery := false
	for key := range r.URL.Query() {
		if key != "cursor" && key != "limit" {
			unknownQuery = true
			break
		}
	}
	if !reviewSHA.MatchString(projectID) || unknownQuery || !oneValue(r.URL.Query(), "cursor", "limit") {
		http.Error(w, "invalid project or query", http.StatusBadRequest)
		return
	}
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 100 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = v
	}
	if !h.catalogActionsAvailable() {
		writeNoStoreJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog version history unavailable"})
		return
	}
	page, err := contextcatalog.ListVersions(h.aiReviewConfig.CatalogDir, projectID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeInternalError(w, "unable to load catalog versions")
		return
	}
	policy, policyErr := contextcatalog.LoadProjectPolicy(h.aiReviewConfig.CatalogDir, projectID)
	if policyErr != nil {
		writeInternalError(w, "unable to load project publication policy")
		return
	}
	currentHash := ""
	var currentEntry *reviewEntry
	snapshot, currentHash, loadErr := contextcatalog.LoadActive(h.aiReviewConfig.CatalogDir)
	if loadErr != nil {
		writeInternalError(w, "unable to load current catalog")
		return
	}
	policy.CurrentEntryHash = ""
	for _, entry := range snapshot.Entries {
		if entry.ProjectID == projectID {
			safe := safeReviewEntry(entry)
			currentEntry = &safe
			entryBytes, marshalErr := json.Marshal(entry)
			if marshalErr != nil {
				writeInternalError(w, "unable to bind current project entry")
				return
			}
			entryHash := sha256.Sum256(entryBytes)
			policy.CurrentEntryHash = hex.EncodeToString(entryHash[:])
			break
		}
	}
	writeNoStoreJSON(w, struct {
		contextcatalog.VersionPage
		CurrentHash  string                       `json:"current_hash"`
		CurrentEntry *reviewEntry                 `json:"current_entry,omitempty"`
		Policy       contextcatalog.ProjectPolicy `json:"policy"`
	}{page, currentHash, currentEntry, policy})
}

func decodeCatalogAction(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCatalogActionBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxCatalogActionBody {
		return errors.New("request body is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func (h *Handler) reviewCatalogWriteAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !h.catalogActionsAvailable() {
		writeNoStoreJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog operations unavailable"})
		return false
	}
	if !validReviewOrigin(r, h.aiReviewOrigin) {
		http.Error(w, "origin rejected", http.StatusForbidden)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func (h *Handler) apiAIReviewRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !h.reviewCatalogWriteAllowed(w, r) {
		return
	}
	var req contextcatalog.RestoreRequest
	if err := decodeCatalogAction(r, &req); err != nil || req.ProjectID != chi.URLParam(r, "project_id") || !validRestoreRequest(req, false) {
		http.Error(w, "invalid restore preview", http.StatusBadRequest)
		return
	}
	preview, err := contextcatalog.PreviewRestore(h.aiReviewConfig.CatalogDir, req)
	if err != nil {
		writeInternalError(w, "unable to preview restore")
		return
	}
	status := http.StatusOK
	if preview.Status != "ready" {
		status = http.StatusConflict
	}
	writeNoStoreJSONStatus(w, status, preview)
}

func validRestoreRequest(req contextcatalog.RestoreRequest, operation bool) bool {
	return reviewSHA.MatchString(req.ProjectID) && reviewSHA.MatchString(req.HistoricalHash) &&
		reviewSHA.MatchString(req.HistoricalEntryHash) && reviewSHA.MatchString(req.ExpectedCurrentHash) &&
		reviewSHA.MatchString(req.ExpectedCurrentEntryHash) && (!operation || operationIDPattern.MatchString(req.OperationID)) &&
		len(req.Reason) <= 512 && (!operation || len(req.Reason) >= 1)
}

func (h *Handler) apiAIReviewRestore(w http.ResponseWriter, r *http.Request) {
	if !h.reviewCatalogWriteAllowed(w, r) {
		return
	}
	var req contextcatalog.RestoreRequest
	if err := decodeCatalogAction(r, &req); err != nil || req.ProjectID != chi.URLParam(r, "project_id") || !validRestoreRequest(req, true) {
		http.Error(w, "invalid restore request", http.StatusBadRequest)
		return
	}
	result, err := contextcatalog.Restore(h.aiReviewConfig.CatalogDir, req)
	if err != nil {
		writeInternalError(w, "unable to restore catalog description")
		return
	}
	status := http.StatusOK
	if result.Status != "restored" && result.Status != "applied" && result.Status != "duplicate" {
		status = http.StatusConflict
	}
	writeNoStoreJSONStatus(w, status, result)
}

func (h *Handler) apiAIReviewOperation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "operation_id")
	if !operationIDPattern.MatchString(id) {
		http.Error(w, "invalid operation id", http.StatusBadRequest)
		return
	}
	if !h.catalogActionsAvailable() {
		writeNoStoreJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog operations unavailable"})
		return
	}
	record, err := contextcatalog.LoadOperation(h.aiReviewConfig.CatalogDir, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			writeInternalError(w, "unable to load catalog operation")
		}
		return
	}
	writeNoStoreJSON(w, record)
}

type publicationHoldRequest struct {
	Hold                     bool   `json:"hold"`
	Reason                   string `json:"reason"`
	ExpectedPolicyRevision   uint64 `json:"expected_policy_revision"`
	ExpectedCurrentEntryHash string `json:"expected_current_entry_hash"`
}

func (h *Handler) apiAIReviewSetHold(w http.ResponseWriter, r *http.Request) {
	if !h.reviewCatalogWriteAllowed(w, r) {
		return
	}
	id := chi.URLParam(r, "project_id")
	if !reviewSHA.MatchString(id) {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req publicationHoldRequest
	if err := decodeCatalogAction(r, &req); err != nil || !reviewSHA.MatchString(req.ExpectedCurrentEntryHash) {
		http.Error(w, "invalid publication hold request", http.StatusBadRequest)
		return
	}
	if len(req.Reason) == 0 || len(req.Reason) > 512 {
		http.Error(w, "reason must contain 1 to 512 bytes", http.StatusBadRequest)
		return
	}
	updated, err := contextcatalog.SetPublicationHold(h.aiReviewConfig.CatalogDir, id, req.Hold, req.Reason, req.ExpectedCurrentEntryHash, req.ExpectedPolicyRevision)
	if errors.Is(err, contextcatalog.ErrConflict) {
		writeNoStoreJSONStatus(w, http.StatusConflict, map[string]string{"error": "catalog changed; refresh and review before confirming"})
		return
	}
	if err != nil {
		writeInternalError(w, "unable to update publication hold")
		return
	}
	writeNoStoreJSON(w, updated)
}
