package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/maintenance"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var recommendationRefPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var recommendationHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// WriteRecommendation exposes validated shadow judgments, never journal contents.
type WriteRecommendation struct {
	OperationRef           string   `json:"operation_ref"`
	Status                 string   `json:"status"`
	Applied                bool     `json:"applied"`
	SuggestedPrimaryTag    string   `json:"suggested_primary_tag,omitempty"`
	RecommendedRelatedTags []string `json:"recommended_related_tags"`
	SubjectDecision        string   `json:"subject_decision,omitempty"`
	ReasonCode             string   `json:"reason_code,omitempty"`
	MissingContextCodes    []string `json:"missing_context_codes,omitempty"`
}

func (s *Server) registerWriteRecommendationTool(srv *server.MCPServer) {
	a := s.aiState()
	if a == nil || a.cfg.Write.Mode != "shadow" {
		return
	}
	srv.AddTool(mcp.NewTool("get_write_recommendation",
		mcp.WithDescription("Read the existing shadow write recommendation using store_fact.ai.operation_ref. Does not call a model or apply tags. Pending permits one delayed follow-up; keep the reference for a later explicit check. Outdated results carry no suggestions. Uses this deployment's configured memory owner; shared credentials are not separate identities."),
		mcp.WithOutputSchema[WriteRecommendation](),
		mcp.WithSchemaAdditionalProperties(false),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("operation_ref", mcp.Required(), mcp.Description("Exact 32-character lowercase hexadecimal reference returned by a shadow store_fact operation")),
	), s.getWriteRecommendation)
}

func (s *Server) getWriteRecommendation(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ref, ok := args["operation_ref"].(string)
	if !ok || len(args) != 1 || !recommendationRefPattern.MatchString(ref) {
		return mcp.NewToolResultError("operation_ref must be exactly 32 lowercase hexadecimal characters; no other arguments are accepted"), nil
	}
	out := s.writeRecommendation(ctx, ref)
	raw, _ := json.Marshal(out)
	return mcp.NewToolResultStructured(out, string(raw)), nil
}

func recommendationResult(ref, status, reason string) WriteRecommendation {
	return WriteRecommendation{OperationRef: ref, Status: status, ReasonCode: reason, RecommendedRelatedTags: []string{}}
}

// Fingerprint excludes recall telemetry and includes the random server-created
// shadow generation. Import drops this marker even when creation time is replayed.
func recommendationFingerprint(payload map[string]interface{}) (string, error) {
	selected := map[string]interface{}{}
	for _, key := range []string{"text", "user", "namespace", "tags", "primary_tag", "origin", "subject", "ai_shadow_generation", "created_at", "permanent", "valid_until", "lifecycle_state", "canonical", "provenance", "verified_at", "lifecycle_transitioned_at", "supersedes", "superseded_by", "maintenance_status"} {
		if value, exists := payload[key]; exists {
			selected[key] = value
		}
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return "", err
	}
	// Normalize nested typed structs and decoded payload maps to the same
	// canonical JSON key order before hashing their equivalent values.
	var normalized interface{}
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", err
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Check directories without creating, chmodding or following symlinks. Local
// state remains operator controlled; callers never provide any part of its path.
func recommendationStateDirectory(dir string) error {
	full, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(full, current), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe state directory")
		}
		if current == full && info.Mode().Perm()&0077 != 0 {
			return errors.New("private state directory required")
		}
	}
	return nil
}

func readRecommendationJournal(dir, ref string) (aiWriteJournal, error) {
	var j aiWriteJournal
	if err := recommendationStateDirectory(dir); err != nil {
		return j, err
	}
	path := filepath.Join(dir, ref+".json")
	// Preserve not-found without conflating a private-file validation failure.
	if _, err := os.Lstat(path); err != nil {
		return j, err
	}
	raw, err := aipolicy.ReadPrivate(path, 64<<10)
	if err != nil {
		return j, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&j); err != nil {
		return j, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return j, errors.New("invalid journal")
	}
	if j.SchemaVersion != 3 || j.Mode != "shadow" || j.OperationRef != ref || j.PointID == "" || j.Owner == "" ||
		!recommendationHashPattern.MatchString(j.InputFingerprint) || !recommendationRefPattern.MatchString(j.RuntimeInstance) ||
		(j.CatalogHash != "" && !recommendationHashPattern.MatchString(j.CatalogHash)) {
		return j, errors.New("unsupported journal")
	}
	switch j.Status {
	case "pending", "decided", "abstained", "failed":
	default:
		return j, errors.New("invalid status")
	}
	if _, err := time.Parse(time.RFC3339Nano, j.PendingUntil); err != nil {
		return j, errors.New("invalid deadline")
	}
	return j, nil
}

func (s *Server) writeRecommendation(ctx context.Context, ref string) WriteRecommendation {
	unavailable := recommendationResult(ref, "unavailable", "result_unavailable")
	a := s.aiState()
	if a == nil || a.cfg.Write.Mode != "shadow" || a.ctx.Err() != nil {
		return unavailable
	}
	ctx, cancel := context.WithTimeout(ctx, aiOperationTimeout)
	defer cancel()
	j, err := readRecommendationJournal(filepath.Join(a.cfg.StateDir, "write-audits"), ref)
	if os.IsNotExist(err) {
		return recommendationResult(ref, "not_found", "")
	}
	if err != nil {
		return unavailable
	}
	if j.Owner != s.user {
		return recommendationResult(ref, "not_found", "")
	}
	point, found, err := s.qdrant.Get(ctx, j.PointID)
	if err != nil || ctx.Err() != nil {
		return unavailable
	}
	if !found {
		return recommendationResult(ref, "outdated", "fact_changed")
	}
	if owner, _ := point.Payload["user"].(string); owner != s.user {
		return recommendationResult(ref, "not_found", "")
	}
	fingerprint, err := recommendationFingerprint(point.Payload)
	if err != nil {
		return unavailable
	}
	if fingerprint != j.InputFingerprint || !maintenance.IsActive(point.Payload) ||
		!lifecycle.IsCurrentTruth(lifecycleView(point.ID, point.Payload), isExpired(point.Payload)) {
		return recommendationResult(ref, "outdated", "fact_changed")
	}
	// Configuration replacement invalidates observations from the old runtime.
	if s.aiState() != a || a.ctx.Err() != nil {
		return unavailable
	}
	if j.Status == "failed" {
		if !recommendationReasonAllowed(j.ReasonCode) {
			return unavailable
		}
		return recommendationResult(ref, "failed", j.ReasonCode)
	}
	catalog, hash, reason := a.catalogWithReason()
	if reason != "" {
		return unavailable
	}
	if hash != j.CatalogHash {
		return recommendationResult(ref, "outdated", "catalog_changed")
	}
	if j.Status == "pending" {
		if j.RuntimeInstance != a.instanceID {
			return recommendationResult(ref, "failed", "interrupted")
		}
		until, _ := time.Parse(time.RFC3339Nano, j.PendingUntil)
		if !time.Now().Before(until) {
			return recommendationResult(ref, "failed", "result_expired")
		}
		return recommendationResult(ref, "pending", "")
	}
	if !recommendationSubjectAllowed(j.SubjectDecision) || !recommendationMissingAllowed(j.MissingContextCodes) {
		return unavailable
	}
	out := recommendationResult(ref, j.Status, "")
	out.SubjectDecision = j.SubjectDecision
	out.MissingContextCodes = append([]string(nil), j.MissingContextCodes...)
	if j.Status == "abstained" {
		if j.SubjectDecision == "known_project" {
			return unavailable
		}
		return out
	}
	if j.SubjectDecision != "known_project" {
		return unavailable
	}
	allowed := map[string]bool{}
	for _, entry := range catalog.Entries {
		if entry.Namespace == "projects" {
			allowed[entry.Tag] = true
		}
	}
	if !allowed[j.After.Primary] || len(j.RecommendedRelatedTags) > 32 {
		return unavailable
	}
	seen := map[string]bool{j.After.Primary: true}
	for _, tag := range j.RecommendedRelatedTags {
		if !allowed[tag] || seen[tag] {
			return unavailable
		}
		seen[tag] = true
	}
	out.SuggestedPrimaryTag = j.After.Primary
	out.RecommendedRelatedTags = append(out.RecommendedRelatedTags, j.RecommendedRelatedTags...)
	return out
}

func recommendationSubjectAllowed(code string) bool {
	switch code {
	case "known_project", "non_project", "other_project", "multiple_projects", "insufficient_context":
		return true
	}
	return false
}
func recommendationMissingAllowed(codes []string) bool {
	if len(codes) > 1 {
		return false
	}
	for _, code := range codes {
		if code != "insufficient_context" {
			return false
		}
	}
	return true
}
func recommendationReasonAllowed(code string) bool {
	switch code {
	case "timeout", "canceled", "provider_auth", "provider_rate_limit", "provider_http", "invalid_response", "provider_transport", "provider_unavailable", "egress_denied", "catalog_unavailable", "invalid_request", "budget_unavailable", "queue_full", "catalog_changed":
		return true
	}
	return false
}
