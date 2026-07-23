package lifecyclemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type fakeStore struct {
	points       map[string]qdrant.Point
	operations   []string
	setCalls     int
	failSetCall  int
	failGetPoint string
}

func (s *fakeStore) ScrollAll(context.Context, map[string]interface{}, bool) ([]qdrant.ScrollPoint, error) {
	result := make([]qdrant.ScrollPoint, 0, len(s.points))
	for _, point := range s.points {
		result = append(result, qdrant.ScrollPoint{ID: point.ID, Payload: cloneMap(point.Payload), Vector: point.Vector})
	}
	return result, nil
}

func (s *fakeStore) Get(_ context.Context, id string) (qdrant.Point, bool, error) {
	if id == s.failGetPoint {
		return qdrant.Point{}, false, errors.New("forced get failure")
	}
	point, found := s.points[id]
	point.Payload = cloneMap(point.Payload)
	return point, found, nil
}

func (s *fakeStore) SetPayload(_ context.Context, id string, payload map[string]interface{}) error {
	s.setCalls++
	if s.failSetCall > 0 && s.setCalls == s.failSetCall {
		return errors.New("forced set failure")
	}
	point := s.points[id]
	for key, value := range payload {
		point.Payload[key] = value
	}
	s.points[id] = point
	s.operations = append(s.operations, "set:"+id)
	return nil
}

func (s *fakeStore) ReplaceLifecyclePayload(_ context.Context, id string, set map[string]interface{}, deleteKeys []string) error {
	point := s.points[id]
	for key, value := range set {
		point.Payload[key] = value
	}
	for _, key := range deleteKeys {
		delete(point.Payload, key)
	}
	s.points[id] = point
	s.operations = append(s.operations, "replace:"+id)
	return nil
}

func migrationFixture() *fakeStore {
	return &fakeStore{points: map[string]qdrant.Point{
		"123": {
			ID:     "123",
			Vector: []float32{0.1, 0.2},
			Payload: map[string]interface{}{
				"text":         "PRIVATE_NUMERIC_FACT",
				"recall_count": float64(7),
			},
		},
		"11111111-1111-1111-1111-111111111111": {
			ID:     "11111111-1111-1111-1111-111111111111",
			Vector: []float32{0.3, 0.4},
			Payload: map[string]interface{}{
				"text":        "PRIVATE_UUID_FACT",
				"permanent":   true,
				"valid_until": "2020-01-01",
			},
		},
		"22222222-2222-2222-2222-222222222222": {
			ID: "22222222-2222-2222-2222-222222222222",
			Payload: map[string]interface{}{
				"text":            "explicit",
				"lifecycle_state": "historical",
			},
		},
		"33333333-3333-3333-3333-333333333333": {
			ID: "33333333-3333-3333-3333-333333333333",
			Payload: map[string]interface{}{
				"text":            "invalid",
				"lifecycle_state": "wrong",
			},
		},
	}}
}

func TestRunDefaultsToPrivacySafeDryRun(t *testing.T) {
	store := migrationFixture()
	report, err := Run(context.Background(), store, Options{Collection: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "dry-run" || report.Scanned != 4 || report.Planned != 2 || report.Invalid != 1 {
		t.Fatalf("report = %#v", report)
	}
	if len(store.operations) != 0 {
		t.Fatalf("dry run operations = %#v", store.operations)
	}
	if got, want := report.PointIDs, []string{"11111111-1111-1111-1111-111111111111", "123"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("point IDs = %#v, want %#v", got, want)
	}
}

func TestApplyResumeAndRollbackKeepImmutableManifest(t *testing.T) {
	store := migrationFixture()
	manifestPath := filepath.Join(t.TempDir(), "rollback.jsonl")
	store.failSetCall = 2
	first, err := Run(context.Background(), store, Options{Collection: "memory", Apply: true, ManifestPath: manifestPath})
	if err == nil || first.Applied != 1 {
		t.Fatalf("first apply report=%#v err=%v", first, err)
	}
	beforeResume, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", info.Mode().Perm())
	}
	for _, secret := range []string{"PRIVATE_NUMERIC_FACT", "PRIVATE_UUID_FACT", "0.1", "recall_count"} {
		if strings.Contains(string(beforeResume), secret) {
			t.Fatalf("manifest leaked %q: %s", secret, beforeResume)
		}
	}

	store.failSetCall = 0
	second, err := Run(context.Background(), store, Options{Collection: "memory", Apply: true, ManifestPath: manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied != 1 || second.AlreadyApplied != 1 {
		t.Fatalf("resume report = %#v", second)
	}
	afterResume, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeResume, afterResume) {
		t.Fatal("resume rewrote immutable manifest")
	}

	rolledBack, err := Run(context.Background(), store, Options{Collection: "memory", RollbackPath: manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.RolledBack != 2 {
		t.Fatalf("rollback report = %#v", rolledBack)
	}
	for _, id := range []string{"123", "11111111-1111-1111-1111-111111111111"} {
		if !isLegacy(store.points[id].Payload) {
			t.Fatalf("point %s was not restored to legacy: %#v", id, store.points[id].Payload)
		}
	}
}

func TestApplyRefusesToReplaceExistingManifest(t *testing.T) {
	store := migrationFixture()
	manifestPath := filepath.Join(t.TempDir(), "rollback.jsonl")
	if err := os.WriteFile(manifestPath, []byte("not a manifest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(manifestPath)
	if _, err := Run(context.Background(), store, Options{Collection: "memory", Apply: true, ManifestPath: manifestPath}); err == nil {
		t.Fatal("apply accepted invalid existing manifest")
	}
	after, _ := os.ReadFile(manifestPath)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("existing manifest was replaced")
	}
	if len(store.operations) != 0 {
		t.Fatalf("operations = %#v", store.operations)
	}
}

func TestRollbackPreservesPostMigrationLifecycleChanges(t *testing.T) {
	store := migrationFixture()
	manifestPath := filepath.Join(t.TempDir(), "rollback.jsonl")
	if _, err := Run(context.Background(), store, Options{Collection: "memory", Apply: true, ManifestPath: manifestPath}); err != nil {
		t.Fatal(err)
	}
	changed := store.points["123"]
	changed.Payload["lifecycle_state"] = "historical"
	store.points["123"] = changed

	report, err := Run(context.Background(), store, Options{Collection: "memory", RollbackPath: manifestPath})
	if err == nil || report.Conflicts != 1 || report.RolledBack != 1 {
		t.Fatalf("rollback report=%#v err=%v", report, err)
	}
	if got := store.points["123"].Payload["lifecycle_state"]; got != "historical" {
		t.Fatalf("post-migration change was overwritten: %#v", store.points["123"].Payload)
	}
}

func TestRunRejectsManifestThatCanMutateNonLegacyMetadata(t *testing.T) {
	store := migrationFixture()
	path := filepath.Join(t.TempDir(), "rollback.jsonl")
	entries, _, _, err := scan(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Before["provenance"] = fieldSnapshot{
		Present: true,
		Value:   map[string]interface{}{"source": "tampered"},
	}
	header := manifestHeader{
		Type:         "header",
		Version:      manifestVersion,
		Collection:   "memory",
		PlanChecksum: checksumEntries(entries),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := createManifest(path, header, entries); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), store, Options{Collection: "memory", Apply: true, ManifestPath: path}); err == nil {
		t.Fatal("apply accepted a non-legacy before snapshot")
	}
	if len(store.operations) != 0 {
		t.Fatalf("operations = %#v", store.operations)
	}
}
