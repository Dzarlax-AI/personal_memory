package factgrouping

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestIsolatedQdrantGroupingApplyRollback(t *testing.T) {
	endpoint := os.Getenv("OPTIONAL_AI_TEST_QDRANT_URL")
	if endpoint == "" {
		t.Skip("isolated Qdrant not requested")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("isolated loopback Qdrant required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := qdrant.NewClient(endpoint, fmt.Sprintf("ai_eval_%d", time.Now().UnixNano()))
	if err := c.CreateCollection(ctx, 4, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.DeleteCollection(context.Background(), "ai_eval_"); err != nil {
			t.Error(err)
		}
	})
	id := "c82c99d3-687c-40f2-a36a-5f1ea9a9c601"
	p := qdrant.Point{ID: id, Vector: []float32{1, 0, 0, 0}, Payload: map[string]interface{}{
		"text": "Synthetic isolated grouping fact", "namespace": "projects", "tags": []string{"alpha"}, "primary_tag": "alpha",
		"lifecycle_state": "current", "permanent": true, "recall_count": 7,
	}}
	if err := c.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	before, found, err := c.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("get before: %v", err)
	}
	fingerprint, err := Fingerprint(before.Payload)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{SchemaVersion: 1, ID: "isolated-review", CatalogHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Approved: true, Changes: []Change{{PointID: id, Fingerprint: fingerprint, Before: GroupFrom(before.Payload), After: Group{Tags: []string{"beta"}, PrimaryTag: "beta", AuditRef: "isolated-review"}}}}
	d := privateDir(t)
	if _, err := Run(ctx, c, m, filepath.Join(d, "apply.json"), true, false); err != nil {
		t.Fatal(err)
	}
	after, _, err := c.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	afterFingerprint, _ := Fingerprint(after.Payload)
	if afterFingerprint != fingerprint || !reflect.DeepEqual(before.Vector, after.Vector) || !reflect.DeepEqual(GroupFrom(after.Payload), m.Changes[0].After) {
		t.Fatal("apply changed unrelated state or missed grouping")
	}
	if _, err := Run(ctx, c, m, filepath.Join(d, "rollback.json"), true, true); err != nil {
		t.Fatal(err)
	}
	restored, _, err := c.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	restoredFingerprint, _ := Fingerprint(restored.Payload)
	if restoredFingerprint != fingerprint || !reflect.DeepEqual(before.Vector, restored.Vector) || !reflect.DeepEqual(GroupFrom(restored.Payload), m.Changes[0].Before) {
		t.Fatal("rollback did not preserve original state")
	}
}
