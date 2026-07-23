package lifecyclemigration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestLifecycleMigrationIntegrationQdrant(t *testing.T) {
	qdrantURL := os.Getenv("QDRANT_TEST_URL")
	if qdrantURL == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	collection := fmt.Sprintf("lifecycle_migration_test_%d", time.Now().UnixNano())
	client := qdrant.NewClient(qdrantURL, collection)
	if err := client.EnsureCollection(ctx, 2); err != nil {
		t.Fatalf("create test collection: %v", err)
	}
	t.Cleanup(func() {
		request, _ := http.NewRequest(http.MethodDelete, qdrantURL+"/collections/"+collection, nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
	})

	fixtures := []qdrant.Point{
		{ID: "123", Vector: []float32{0.1, 0.2}, Payload: map[string]interface{}{"text": "numeric private", "recall_count": 9}},
		{ID: "11111111-1111-1111-1111-111111111111", Vector: []float32{0.3, 0.4}, Payload: map[string]interface{}{"text": "uuid private", "permanent": true}},
	}
	for _, point := range fixtures {
		if err := client.Upsert(ctx, point); err != nil {
			t.Fatalf("upsert fixture %s: %v", point.ID, err)
		}
	}
	baselines := make(map[string]qdrant.Point, len(fixtures))
	for _, fixture := range fixtures {
		point, found, err := client.Get(ctx, fixture.ID)
		if err != nil || !found {
			t.Fatalf("get baseline %s: found=%v err=%v", fixture.ID, found, err)
		}
		baselines[fixture.ID] = point
	}

	dryRun, err := Run(ctx, client, Options{Collection: collection})
	if err != nil || dryRun.Planned != 2 {
		t.Fatalf("dry run report=%#v err=%v", dryRun, err)
	}
	for _, fixture := range fixtures {
		point, found, err := client.Get(ctx, fixture.ID)
		if err != nil || !found || !isLegacy(point.Payload) {
			t.Fatalf("dry run changed %s: point=%#v found=%v err=%v", fixture.ID, point, found, err)
		}
	}

	manifestPath := t.TempDir() + "/rollback.jsonl"
	applied, err := Run(ctx, client, Options{Collection: collection, Apply: true, ManifestPath: manifestPath})
	if err != nil || applied.Applied != 2 {
		t.Fatalf("apply report=%#v err=%v", applied, err)
	}
	resumed, err := Run(ctx, client, Options{Collection: collection, Apply: true, ManifestPath: manifestPath})
	if err != nil || resumed.AlreadyApplied != 2 {
		t.Fatalf("resume report=%#v err=%v", resumed, err)
	}
	for _, fixture := range fixtures {
		point, found, err := client.Get(ctx, fixture.ID)
		if err != nil || !found {
			t.Fatalf("get applied %s: found=%v err=%v", fixture.ID, found, err)
		}
		baseline := baselines[fixture.ID]
		if point.Payload["text"] != baseline.Payload["text"] || !reflect.DeepEqual(point.Vector, baseline.Vector) {
			t.Fatalf("apply changed text/vector for %s: %#v", fixture.ID, point)
		}
	}

	rolledBack, err := Run(ctx, client, Options{Collection: collection, RollbackPath: manifestPath})
	if err != nil || rolledBack.RolledBack != 2 {
		t.Fatalf("rollback report=%#v err=%v", rolledBack, err)
	}
	for _, fixture := range fixtures {
		point, found, err := client.Get(ctx, fixture.ID)
		if err != nil || !found || !isLegacy(point.Payload) {
			t.Fatalf("rollback did not restore %s: point=%#v found=%v err=%v", fixture.ID, point, found, err)
		}
		baseline := baselines[fixture.ID]
		if point.Payload["text"] != baseline.Payload["text"] || !reflect.DeepEqual(point.Vector, baseline.Vector) {
			t.Fatalf("rollback changed text/vector for %s: %#v", fixture.ID, point)
		}
	}
}
