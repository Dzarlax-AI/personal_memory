package eval

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/Dzarlax-AI/personal-memory/internal/rerank"
)

type rerankerFunc func(context.Context, string, []rerank.Candidate) ([]rerank.Ranked, error)

func (fn rerankerFunc) Rerank(ctx context.Context, query string, candidates []rerank.Candidate) ([]rerank.Ranked, error) {
	return fn(ctx, query, candidates)
}

func TestApplyDocumentRerankerReordersPointsAndTraceTogether(t *testing.T) {
	points := []qdrant.Point{
		{ID: "a", Payload: map[string]any{"text": "A"}},
		{ID: "b", Payload: map[string]any{"text": "B"}},
		{ID: "c", Payload: map[string]any{"text": "C"}},
	}
	trace := &RoutingTrace{Results: []RoutingResultTrace{
		{ID: "a", Sources: []RoutingSourceTrace{{Source: "flat", Rank: 1}}},
		{ID: "b", Sources: []RoutingSourceTrace{{Source: "flat", Rank: 2}}},
		{ID: "c", Sources: []RoutingSourceTrace{{Source: "flat", Rank: 3}}},
	}}
	service := rerankerFunc(func(context.Context, string, []rerank.Candidate) ([]rerank.Ranked, error) {
		return []rerank.Ranked{{Index: 2}, {Index: 0}, {Index: 1}}, nil
	})

	got := applyDocumentReranker(context.Background(), service, "query", time.Second, 3, points, trace)
	if ids := pointIDsForEval(got); !reflect.DeepEqual(ids, []string{"c", "a", "b"}) {
		t.Fatalf("point IDs = %v", ids)
	}
	traceIDs := []string{trace.Results[0].ID, trace.Results[1].ID, trace.Results[2].ID}
	if !reflect.DeepEqual(traceIDs, []string{"c", "a", "b"}) || trace.RerankerReason != rerank.ReasonApplied {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestApplyDocumentRerankerHonorsDeclaredTimeoutAndFailsOpen(t *testing.T) {
	points := []qdrant.Point{{ID: "a", Payload: map[string]any{"text": "A"}}}
	trace := &RoutingTrace{Results: []RoutingResultTrace{{ID: "a"}}}
	service := rerankerFunc(func(ctx context.Context, _ string, _ []rerank.Candidate) ([]rerank.Ranked, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	started := time.Now()

	got := applyDocumentReranker(context.Background(), service, "query", 10*time.Millisecond, 1, points, trace)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("declared reranker timeout was not honored: %v", elapsed)
	}
	if ids := pointIDsForEval(got); !reflect.DeepEqual(ids, []string{"a"}) || trace.RerankerReason != rerank.ReasonFallback {
		t.Fatalf("fallback points/trace = %v/%#v", ids, trace)
	}
}
