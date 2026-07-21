package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func newRecallCounterTestServer(t *testing.T, initial int) (*Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	count := initial
	qs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/points/fact-id"):
			_, _ = fmt.Fprintf(w, `{"result":{"id":"fact-id","payload":{"recall_count":%d}}}`, count)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/payload"):
			var body struct {
				Payload map[string]interface{} `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode payload: %v", err)
				return
			}
			count = int(body.Payload["recall_count"].(float64))
			_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
		default:
			t.Errorf("unexpected Qdrant request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(qs.Close)

	cache := NewCache(time.Minute)
	cache.Set("query||[]|1|"+lifecycleCacheScope, []map[string]interface{}{{
		"_point_id": "fact-id", "text": "cached fact", "score": 0.99,
		"namespace": "personal", "recall_count": float64(initial),
	}})
	srv := &Server{qdrant: qdrant.NewClient(qs.URL, "memory"), cache: cache}
	srv.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestRecallCounterRetriesTransientWriteFailure(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	qs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"id":"fact-id","payload":{"recall_count":7}}}`))
			return
		}
		mu.Lock()
		posts++
		attempt := posts
		mu.Unlock()
		if attempt == 1 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
	}))
	defer qs.Close()
	counter := newRecallCounter(context.Background(), qdrant.NewClient(qs.URL, "memory"), 2, 5*time.Millisecond)
	if err := counter.enqueue(context.Background(), "fact-id"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		got := posts
		mu.Unlock()
		if got >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("write was not retried; attempts=%d", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := counter.stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecallFactsCacheHitIncrementsWithoutLeakingPointID(t *testing.T) {
	srv, count := newRecallCounterTestServer(t, 4)
	result, err := srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{
		"query": "query", "limit": float64(1),
	}))
	if err != nil || result.IsError {
		t.Fatalf("recall failed: result=%#v err=%v", result, err)
	}
	if strings.Contains(toolResultText(t, result), "fact-id") {
		t.Fatal("internal point ID leaked in formatted recall output")
	}
	if !strings.Contains(toolResultText(t, result), "recalls:5") {
		t.Fatalf("cache-visible recall count was not advanced: %q", toolResultText(t, result))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 5 {
		t.Fatalf("recall_count = %d, want 5", got)
	}
}

func TestRecallCounterCoalescesConcurrentIncrementsAndDrains(t *testing.T) {
	srv, count := newRecallCounterTestServer(t, 2)
	const recalls = 100
	var wg sync.WaitGroup
	for i := 0; i < recalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{
				"query": "query", "limit": float64(1),
			}))
			if err != nil || result.IsError {
				t.Errorf("recall failed: result=%#v err=%v", result, err)
			}
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 2+recalls {
		t.Fatalf("recall_count = %d, want %d", got, 2+recalls)
	}
}

func TestRecallCounterAppliesBackpressureInsteadOfDropping(t *testing.T) {
	blockGet := make(chan struct{})
	enteredGet := make(chan struct{})
	var enteredOnce sync.Once
	qs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			enteredOnce.Do(func() { close(enteredGet) })
			<-blockGet
			_, _ = w.Write([]byte(`{"result":{"id":"first","payload":{"recall_count":0}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
	}))
	defer qs.Close()
	counter := newRecallCounter(context.Background(), qdrant.NewClient(qs.URL, "memory"), 1, time.Millisecond)
	if err := counter.enqueue(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-enteredGet:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin flushing first increment")
	}
	if err := counter.enqueue(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := counter.enqueue(ctx, "third"); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected bounded backpressure deadline, got %v", err)
	}
	close(blockGet)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := counter.stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}
