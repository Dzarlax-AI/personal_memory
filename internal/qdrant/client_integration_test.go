package qdrant

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// TestSearchQueryEndpointAgainstQdrant exercises the query-points REST API
// against the isolated Qdrant service used by CI. It skips when no test URL is
// configured, so ordinary unit tests never contact a Qdrant instance.
func TestSearchQueryEndpointAgainstQdrant(t *testing.T) {
	qdrantURL := os.Getenv("QDRANT_TEST_URL")
	if qdrantURL == "" {
		t.Skip("QDRANT_TEST_URL is not configured")
	}

	collection := fmt.Sprintf("eval_query_compat_%d", time.Now().UnixNano())
	client := NewClient(qdrantURL, collection)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.CreateCollection(ctx, 2, nil); err != nil {
		t.Fatalf("create isolated query compatibility collection: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := client.DeleteCollection(cleanupCtx, "eval_query_compat_"); err != nil {
			t.Errorf("delete isolated query compatibility collection: %v", err)
		}
	})

	points := []struct {
		point   Point
		numeric bool
	}{
		{point: Point{ID: "42", Vector: []float32{1, 0}, Payload: map[string]any{"namespace": "projects"}}, numeric: true},
		{point: Point{ID: "11111111-1111-4111-8111-111111111111", Vector: []float32{0.8, 0.6}, Payload: map[string]any{"namespace": "projects"}}},
		{point: Point{ID: "99", Vector: []float32{1, 0}, Payload: map[string]any{"namespace": "other"}}, numeric: true},
	}
	for _, item := range points {
		if err := client.UpsertWithPointID(ctx, item.point, item.numeric); err != nil {
			t.Fatalf("upsert isolated point %q: %v", item.point.ID, err)
		}
	}
	threshold := 0.7
	got, err := client.Search(ctx, []float32{1, 0}, 2, map[string]any{
		"must": []any{map[string]any{"key": "namespace", "match": map[string]any{"value": "projects"}}},
	}, &threshold)
	if err != nil {
		t.Fatalf("query isolated collection: %v", err)
	}
	if len(got) != 2 || !reflect.DeepEqual([]string{got[0].ID, got[1].ID}, []string{"42", "11111111-1111-4111-8111-111111111111"}) {
		t.Fatalf("query points = %#v, want filtered numeric and UUID IDs in score order", got)
	}
	if got[0].Score != 1 || got[1].Score < threshold || got[0].Payload["namespace"] != "projects" || got[1].Payload["namespace"] != "projects" {
		t.Fatalf("query points lost score, threshold, or payload semantics: %#v", got)
	}
}
