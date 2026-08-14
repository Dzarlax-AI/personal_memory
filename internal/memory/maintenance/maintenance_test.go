package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestWriteManifestIsPrivateAndExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analysis.json")
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, PolicyVersion: PolicyVersion, BatchID: "batch", Collection: "memory", ReferenceTime: "2026-08-14T00:00:00Z", Complete: true, Findings: []Finding{}}
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := WriteManifest(path, manifest); err == nil {
		t.Fatal("expected exclusive-create refusal")
	}
}

func TestParseStatus(t *testing.T) {
	now := "2026-08-14T10:00:00Z"
	tests := []struct {
		name    string
		payload map[string]interface{}
		status  Status
		legacy  bool
		valid   bool
	}{
		{name: "legacy active", payload: map[string]interface{}{}, status: Active, legacy: true, valid: true},
		{name: "explicit active", payload: map[string]interface{}{"maintenance_status": "active"}, status: Active, valid: true},
		{name: "quarantined", payload: map[string]interface{}{"maintenance_status": "quarantined", "quarantined_at": now, "quarantine_reason": "expired", "quarantine_batch_id": "batch-1"}, status: Quarantined, valid: true},
		{name: "unknown quarantine reason", payload: map[string]interface{}{"maintenance_status": "quarantined", "quarantined_at": now, "quarantine_reason": "old", "quarantine_batch_id": "batch-1"}},
		{name: "unknown", payload: map[string]interface{}{"maintenance_status": "deleted"}},
		{name: "incomplete quarantine", payload: map[string]interface{}{"maintenance_status": "quarantined", "quarantined_at": now}},
		{name: "active with quarantine fields", payload: map[string]interface{}{"maintenance_status": "active", "quarantined_at": now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.payload)
			if got.Status != tt.status || got.Legacy != tt.legacy || got.Valid != tt.valid {
				t.Fatalf("Parse() = %#v", got)
			}
		})
	}
}

func TestNamespaceScopedAnalyzeDoesNotInventCrossNamespaceOrphans(t *testing.T) {
	points := []qdrant.ScrollPoint{{ID: "1", Payload: map[string]interface{}{
		"namespace": "projects", "lifecycle_state": "superseded", "canonical": false,
		"supersedes": []interface{}{}, "superseded_by": []interface{}{"2"}, "updated_at": "2026-08-13T00:00:00Z", "lifecycle_transitioned_at": "2026-08-13T00:00:00Z",
	}}}
	manifest, err := Analyze(context.Background(), fakeScanner{points: points}, Options{
		Collection: "memory", Namespace: "projects", ReferenceTime: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		SupersededRetention: 30 * 24 * time.Hour, StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Findings) != 0 {
		t.Fatalf("findings = %#v", manifest.Findings)
	}
}

func TestActiveFilterComposesWithoutMutatingBase(t *testing.T) {
	base := map[string]interface{}{"should": []map[string]interface{}{{"key": "lifecycle_state"}}}
	got := ActiveFilter(base)
	encoded, _ := json.Marshal(got)
	for _, want := range []string{"maintenance_status", "active", "is_empty", "lifecycle_state"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("filter %s missing %q", encoded, want)
		}
	}
	if len(base) != 1 {
		t.Fatalf("base mutated: %#v", base)
	}
}

func TestAnalyzeClassifiesConservativelyAndNeverEmitsText(t *testing.T) {
	reference := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"text": "private expired", "namespace": "projects", "created_at": "2026-01-01T00:00:00Z", "valid_until": "2026-08-01", "recall_count": float64(0)}},
		{ID: "2", Payload: map[string]interface{}{"text": "protected fact body", "created_at": "2025-01-01T00:00:00Z", "lifecycle_state": "current", "canonical": true, "supersedes": []interface{}{}, "superseded_by": []interface{}{}, "recall_count": float64(0)}},
		{ID: "3", Payload: map[string]interface{}{"text": "superseded fact body", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2026-08-13T00:00:00Z", "lifecycle_transitioned_at": "2026-06-01T00:00:00Z", "lifecycle_state": "superseded", "canonical": false, "supersedes": []interface{}{}, "superseded_by": []interface{}{"4"}, "recall_count": float64(0)}},
		{ID: "4", Payload: map[string]interface{}{"text": "current", "created_at": "2026-06-01T00:00:00Z", "recall_count": float64(4)}},
		{ID: "5", Payload: map[string]interface{}{"text": "bad metadata", "valid_until": "tomorrow"}},
	}
	manifest, err := Analyze(context.Background(), fakeScanner{points: points}, Options{
		Collection:          "memory",
		ReferenceTime:       reference,
		SupersededRetention: 30 * 24 * time.Hour,
		StaleAfter:          90 * 24 * time.Hour,
		LowRecallThreshold:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Complete || manifest.Scanned != len(points) || manifest.BatchID == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	encoded, _ := json.Marshal(manifest)
	for _, secret := range []string{"private expired", "protected fact body", "superseded fact body", "bad metadata", `"text"`} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("manifest leaked %q: %s", secret, encoded)
		}
	}
	byID := map[string]Finding{}
	for _, finding := range manifest.Findings {
		byID[finding.PointID] = finding
	}
	if !byID["1"].EligibleForQuarantine || !containsClass(byID["1"].Classes, ClassExpired) {
		t.Fatalf("expired finding = %#v", byID["1"])
	}
	if !byID["2"].Protected || byID["2"].EligibleForQuarantine {
		t.Fatalf("protected finding = %#v", byID["2"])
	}
	if !byID["3"].EligibleForQuarantine || !containsClass(byID["3"].Classes, ClassSupersededRetention) {
		t.Fatalf("superseded finding = %#v", byID["3"])
	}
	if !containsClass(byID["5"].Classes, ClassMalformedMetadata) {
		t.Fatalf("malformed finding = %#v", byID["5"])
	}
}

func TestAnalyzeOmitsAllMetadataWhenAnyManifestFieldIsMalformed(t *testing.T) {
	const secret = "PRIVATE_METADATA_MARKER"
	manifest, err := Analyze(context.Background(), fakeScanner{points: []qdrant.ScrollPoint{{
		ID: "1", Payload: map[string]interface{}{
			"text":               secret,
			"lifecycle_state":    secret,
			"created_at":         secret,
			"updated_at":         secret,
			"valid_until":        secret,
			"maintenance_status": secret,
		},
	}}}, Options{
		Collection: "memory", ReferenceTime: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		SupersededRetention: 30 * 24 * time.Hour, StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("manifest leaked malformed metadata: %s", encoded)
	}
	if len(manifest.Findings) != 1 || !containsClass(manifest.Findings[0].Classes, ClassMalformedMetadata) {
		t.Fatalf("findings = %#v", manifest.Findings)
	}
}

func TestSupersededRetentionUsesLifecycleTransitionTime(t *testing.T) {
	reference := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	points := []qdrant.ScrollPoint{
		{ID: "old-transition", Payload: map[string]interface{}{"updated_at": "2026-08-13T00:00:00Z", "lifecycle_transitioned_at": "2026-06-01T00:00:00Z", "lifecycle_state": "superseded", "superseded_by": []interface{}{"current"}}},
		{ID: "new-transition", Payload: map[string]interface{}{"updated_at": "2020-01-01T00:00:00Z", "lifecycle_transitioned_at": "2026-08-13T00:00:00Z", "lifecycle_state": "superseded", "superseded_by": []interface{}{"current"}}},
		{ID: "current", Payload: map[string]interface{}{}},
	}
	manifest, err := Analyze(context.Background(), fakeScanner{points: points}, Options{
		Collection: "memory", ReferenceTime: reference, SupersededRetention: 30 * 24 * time.Hour,
		StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Finding{}
	for _, finding := range manifest.Findings {
		byID[finding.PointID] = finding
	}
	if !byID["old-transition"].EligibleForQuarantine || !containsClass(byID["old-transition"].Classes, ClassSupersededRetention) {
		t.Fatalf("old transition = %#v", byID["old-transition"])
	}
	if containsClass(byID["new-transition"].Classes, ClassSupersededRetention) || byID["new-transition"].EligibleForQuarantine {
		t.Fatalf("new transition = %#v", byID["new-transition"])
	}
}

func TestAgeAndLowRecallAloneAreReviewOnly(t *testing.T) {
	manifest, err := Analyze(context.Background(), fakeScanner{points: []qdrant.ScrollPoint{{
		ID: "1", Payload: map[string]interface{}{"text": "old", "created_at": "2020-01-01T00:00:00Z", "recall_count": float64(0)},
	}}}, Options{Collection: "memory", ReferenceTime: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1, SupersededRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Findings) != 1 || manifest.Findings[0].EligibleForQuarantine || !containsClass(manifest.Findings[0].Classes, ClassStaleUnused) {
		t.Fatalf("findings = %#v", manifest.Findings)
	}
}

type fakeScanner struct {
	points []qdrant.ScrollPoint
	err    error
}

func (f fakeScanner) ScrollAll(context.Context, map[string]interface{}, bool) ([]qdrant.ScrollPoint, error) {
	return f.points, f.err
}

func containsClass(classes []CandidateClass, want CandidateClass) bool {
	for _, class := range classes {
		if class == want {
			return true
		}
	}
	return false
}
