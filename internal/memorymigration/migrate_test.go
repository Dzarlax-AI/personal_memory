package memorymigration

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/memory"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type fakeStore struct {
	points map[string]qdrant.Point
	ops    []string
}

func newFakeStore(points ...qdrant.Point) *fakeStore {
	store := &fakeStore{points: map[string]qdrant.Point{}}
	for _, point := range points {
		store.points[point.ID] = point
	}
	return store
}

func (s *fakeStore) ScrollAll(context.Context, map[string]interface{}, bool) ([]qdrant.ScrollPoint, error) {
	points := make([]qdrant.ScrollPoint, 0, len(s.points))
	for _, point := range s.points {
		points = append(points, qdrant.ScrollPoint{ID: point.ID, Vector: point.Vector, Payload: point.Payload})
	}
	return points, nil
}

func (s *fakeStore) Get(_ context.Context, id string) (qdrant.Point, bool, error) {
	point, found := s.points[id]
	return point, found, nil
}

func (s *fakeStore) Upsert(_ context.Context, point qdrant.Point) error {
	s.ops = append(s.ops, "upsert:"+point.ID)
	s.points[point.ID] = point
	return nil
}

func (s *fakeStore) Delete(_ context.Context, ids []string) error {
	for _, id := range ids {
		s.ops = append(s.ops, "delete:"+id)
		delete(s.points, id)
	}
	return nil
}

func legacyPoint(id, namespace, text string) qdrant.Point {
	payload := map[string]interface{}{"text": text}
	if namespace != "" {
		payload["namespace"] = namespace
	}
	return qdrant.Point{ID: id, Vector: []float32{0.1, 0.2}, Payload: payload}
}

func TestRunDefaultsToLosslessDryRun(t *testing.T) {
	store := newFakeStore(legacyPoint("123", "personal", "fact"))
	report, err := Run(context.Background(), store, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Planned != 1 || report.Migrated != 0 || len(store.ops) != 0 {
		t.Fatalf("unexpected dry-run report/operations: %#v ops=%v", report, store.ops)
	}
	if _, found := store.points["123"]; !found {
		t.Fatal("dry run removed source point")
	}
}

func TestRunWritesBeforeDeleteAndIsIdempotent(t *testing.T) {
	store := newFakeStore(legacyPoint("123", "personal", "fact"))
	target := memory.PointID("personal", "fact")
	report, err := Run(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	wantOps := []string{"upsert:" + target, "delete:123"}
	if fmt.Sprint(store.ops) != fmt.Sprint(wantOps) {
		t.Fatalf("operations = %v, want %v", store.ops, wantOps)
	}
	if report.Migrated != 1 {
		t.Fatalf("report = %#v", report)
	}

	store.ops = nil
	second, err := Run(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.AlreadyCurrent != 1 || len(store.ops) != 0 {
		t.Fatalf("second run not idempotent: %#v ops=%v", second, store.ops)
	}
}

func TestRunResumesAfterWriteBeforeDelete(t *testing.T) {
	legacy := legacyPoint("123", "", "fact")
	legacy.Vector = []float32{0.3, 0.4}
	legacy.Payload["tags"] = []interface{}{"personal-memory", "migration"}
	legacy.Payload["permanent"] = true
	legacy.Payload["created_at"] = "2026-07-19T12:00:00Z"
	targetID := memory.PointID("default", "fact")
	target := qdrant.Point{ID: targetID, Vector: append([]float32(nil), legacy.Vector...), Payload: clonePayload(legacy.Payload)}
	target.Payload["namespace"] = "default"
	store := newFakeStore(legacy, target)
	report, err := Run(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Migrated != 1 || fmt.Sprint(store.ops) != fmt.Sprint([]string{"delete:123"}) {
		t.Fatalf("resume report=%#v ops=%v", report, store.ops)
	}
}

func TestRunConsolidatesEquivalentLegacyDuplicates(t *testing.T) {
	first := legacyPoint("123", "personal", "fact")
	first.Payload["tags"] = []interface{}{"memory"}
	second := legacyPoint("456", "personal", "fact")
	second.Payload["tags"] = []interface{}{"memory"}
	store := newFakeStore(first, second)

	report, err := Run(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	targetID := memory.PointID("personal", "fact")
	if report.Planned != 2 || report.Migrated != 2 || report.Collisions != 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if _, found := store.points["123"]; found {
		t.Fatal("first equivalent source was not removed")
	}
	if _, found := store.points["456"]; found {
		t.Fatal("second equivalent source was not removed")
	}
	if _, found := store.points[targetID]; !found {
		t.Fatal("consolidated target was not written")
	}
}

func TestRunLeavesConflictingLegacyDuplicatesUntouched(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*qdrant.Point)
	}{
		{
			name: "metadata differs",
			mutate: func(point *qdrant.Point) {
				point.Payload["tags"] = []interface{}{"different"}
			},
		},
		{
			name: "vector differs",
			mutate: func(point *qdrant.Point) {
				point.Vector = []float32{0.9, 0.8}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := legacyPoint("123", "personal", "fact")
			second := legacyPoint("456", "personal", "fact")
			test.mutate(&second)
			store := newFakeStore(first, second)

			report, err := Run(context.Background(), store, true)
			if err != nil {
				t.Fatal(err)
			}
			if report.Collisions != 1 || report.Planned != 0 || report.Migrated != 0 || len(store.ops) != 0 {
				t.Fatalf("conflicting sources were not safely skipped: %#v ops=%v", report, store.ops)
			}
			if len(store.points) != 2 {
				t.Fatalf("conflicting sources changed: %#v", store.points)
			}
			if _, found := store.points[memory.PointID("personal", "fact")]; found {
				t.Fatal("target was written despite a source collision")
			}
		})
	}
}

func TestRunLeavesExistingTargetMismatchUntouched(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*qdrant.Point)
	}{
		{
			name: "metadata differs",
			mutate: func(point *qdrant.Point) {
				point.Payload["permanent"] = true
			},
		},
		{
			name: "vector differs",
			mutate: func(point *qdrant.Point) {
				point.Vector = []float32{0.9, 0.8}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := legacyPoint("123", "personal", "fact")
			targetID := memory.PointID("personal", "fact")
			target := legacyPoint(targetID, "personal", "fact")
			test.mutate(&target)
			store := newFakeStore(source, target)

			report, err := Run(context.Background(), store, true)
			if err != nil {
				t.Fatal(err)
			}
			if report.Collisions != 1 || report.Planned != 0 || report.Migrated != 0 || len(store.ops) != 0 {
				t.Fatalf("target mismatch was not safely skipped: %#v ops=%v", report, store.ops)
			}
			if _, found := store.points["123"]; !found {
				t.Fatal("source was removed despite target mismatch")
			}
			if got := store.points[targetID]; !reflect.DeepEqual(got, target) {
				t.Fatalf("target was overwritten: got %#v want %#v", got, target)
			}
		})
	}
}

func TestRunDoesNotOverwriteCollision(t *testing.T) {
	source := legacyPoint("123", "personal", "fact")
	targetID := memory.PointID("personal", "fact")
	collision := legacyPoint(targetID, "work", "different")
	store := newFakeStore(source, collision)
	report, err := Run(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Collisions != 1 || len(store.ops) != 0 {
		t.Fatalf("collision was not safely skipped: %#v ops=%v", report, store.ops)
	}
}
