package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestRecallFactsColdMissSingleflightCountsEveryCallerOnce(t *testing.T) {
	const callers = 32
	var embeds atomic.Int32
	var searches atomic.Int32
	var backendMu sync.Mutex
	backendCount := 0
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		embeds.Add(1)
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	}))
	defer embedServer.Close()
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			searches.Add(1)
			_, _ = w.Write([]byte(`{"result":[{"id":"fact-id","score":0.9,"payload":{"text":"fact","namespace":"projects","recall_count":0}}]}`))
		case r.Method == http.MethodGet:
			backendMu.Lock()
			count := backendCount
			backendMu.Unlock()
			_, _ = fmt.Fprintf(w, `{"result":{"id":"fact-id","payload":{"recall_count":%d}}}`, count)
		case strings.HasSuffix(r.URL.Path, "/points/payload"):
			var body struct {
				Payload map[string]interface{} `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode payload: %v", err)
				return
			}
			backendMu.Lock()
			backendCount = int(body.Payload["recall_count"].(float64))
			backendMu.Unlock()
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer qdrantServer.Close()
	srv := NewServer(qdrant.NewClient(qdrantServer.URL, "memory"), embeddings.NewClient(embedServer.URL), NewCache(time.Minute), "test", .97, .60, .90)
	srv.Start(context.Background())

	start := make(chan struct{})
	counts := make(chan int, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": "fact", "limit": float64(1)}))
			if err != nil || result.IsError {
				t.Errorf("recall failed: %#v %v", result, err)
				return
			}
			counts <- result.StructuredContent.(RecallFactsResult).Facts[0].RecallCount
		}()
	}
	close(start)
	wg.Wait()
	close(counts)
	got := make([]int, 0, callers)
	for count := range counts {
		got = append(got, count)
	}
	sort.Ints(got)
	for index, count := range got {
		if count != index+1 {
			t.Fatalf("visible counts = %v", got)
		}
	}
	if embeds.Load() != 1 || searches.Load() != 1 {
		t.Fatalf("embeds=%d searches=%d, want 1/1", embeds.Load(), searches.Load())
	}
	key := recallFactsCacheKey("fact", "", nil, 1, LifecycleRecallOptions{Mode: RecallLifecycleCurrent})
	cached, ok := srv.cache.GetRecall(key)
	if !ok || cached.Facts[0].RecallCount != callers {
		t.Fatalf("cached=%#v ok=%v", cached, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	backendMu.Lock()
	defer backendMu.Unlock()
	if backendCount != callers {
		t.Fatalf("backend count=%d, want %d", backendCount, callers)
	}
}

func TestRecallFactsInvalidationPreventsStaleLeaderPublishAndWakesWaiter(t *testing.T) {
	firstSearchEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var searches atomic.Int32
	srv := newSingleflightTestServer(t, func(w http.ResponseWriter) {
		search := searches.Add(1)
		if search == 1 {
			close(firstSearchEntered)
			<-releaseFirst
			_, _ = w.Write([]byte(`{"result":[{"id":"old-id","score":0.9,"payload":{"text":"old","recall_count":0}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":[{"id":"new-id","score":0.9,"payload":{"text":"new","recall_count":0}}]}`))
	})
	key := recallFactsCacheKey("fact", "", nil, 1, LifecycleRecallOptions{Mode: RecallLifecycleCurrent})
	results := make(chan string, 2)
	go recallTextForSingleflightTest(t, srv, context.Background(), results)
	<-firstSearchEntered
	go recallTextForSingleflightTest(t, srv, context.Background(), results)
	waitForRecallWaiters(t, srv.cache, key, 1)
	srv.cache.Invalidate()
	select {
	case got := <-results:
		if got != "new" {
			t.Fatalf("woken waiter got %q, want new", got)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not retry after invalidation")
	}
	close(releaseFirst)
	if got := <-results; got != "old" {
		t.Fatalf("old leader returned %q", got)
	}
	cached, ok := srv.cache.GetRecall(key)
	if !ok || cached.Facts[0].Text != "new" {
		t.Fatalf("stale leader repopulated cache: %#v ok=%v", cached, ok)
	}
	if searches.Load() != 2 {
		t.Fatalf("searches=%d, want 2", searches.Load())
	}
}

func TestRecallFactsLeaderErrorWakesWaitersAndCanceledWaiterExits(t *testing.T) {
	firstEmbedEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var embeds atomic.Int32
	srv := newSingleflightEmbedTestServer(t, func(w http.ResponseWriter) {
		if embeds.Add(1) == 1 {
			close(firstEmbedEntered)
			<-releaseFirst
			http.Error(w, "failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	})
	key := recallFactsCacheKey("fact", "", nil, 1, LifecycleRecallOptions{Mode: RecallLifecycleCurrent})
	leaderDone := make(chan *RecallFactsResult, 1)
	go func() {
		result, _ := srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": "fact", "limit": float64(1)}))
		if result.IsError {
			leaderDone <- nil
			return
		}
		value := result.StructuredContent.(RecallFactsResult)
		leaderDone <- &value
	}()
	<-firstEmbedEntered
	follower := make(chan string, 1)
	go recallTextForSingleflightTest(t, srv, context.Background(), follower)
	waitForRecallWaiters(t, srv.cache, key, 1)
	canceledCtx, cancel := context.WithCancel(context.Background())
	canceled := make(chan bool, 1)
	go func() {
		result, _ := srv.recallFacts(canceledCtx, toolRequest(map[string]interface{}{"query": "fact", "limit": float64(1)}))
		canceled <- result.IsError
	}()
	waitForRecallWaiters(t, srv.cache, key, 2)
	cancel()
	if !<-canceled {
		t.Fatal("canceled waiter did not return an error")
	}
	close(releaseFirst)
	if result := <-leaderDone; result != nil {
		t.Fatalf("failed leader unexpectedly succeeded: %#v", result)
	}
	select {
	case got := <-follower:
		if got != "fresh" {
			t.Fatalf("retried follower got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("leader error did not wake follower")
	}
	if embeds.Load() != 2 {
		t.Fatalf("embeds=%d, want failed leader plus retry", embeds.Load())
	}
}

func TestRecallFactsDifferentKeysDoNotShareFlight(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := newSingleflightEmbedTestServer(t, func(w http.ResponseWriter) {
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	})
	done := make(chan string, 2)
	go recallTextForSingleflightTestQuery(t, srv, context.Background(), "first", done)
	go recallTextForSingleflightTestQuery(t, srv, context.Background(), "second", done)
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("different cache keys blocked each other's embed")
		}
	}
	close(release)
	<-done
	<-done
}

func newSingleflightTestServer(t *testing.T, search func(http.ResponseWriter)) *Server {
	t.Helper()
	return newSingleflightServers(t, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`[[0.1,0.2]]`)) }, search)
}

func newSingleflightEmbedTestServer(t *testing.T, embed func(http.ResponseWriter)) *Server {
	t.Helper()
	return newSingleflightServers(t, embed, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"result":[{"id":"fact-id","score":0.9,"payload":{"text":"fresh","recall_count":0}}]}`))
	})
}

func newSingleflightServers(t *testing.T, embed func(http.ResponseWriter), search func(http.ResponseWriter)) *Server {
	t.Helper()
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { embed(w) }))
	t.Cleanup(embedServer.Close)
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			search(w)
		case r.Method == http.MethodGet:
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_, _ = fmt.Fprintf(w, `{"result":{"id":%q,"payload":{"recall_count":0}}}`, id)
		case strings.HasSuffix(r.URL.Path, "/points/payload"):
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(qdrantServer.Close)
	srv := NewServer(qdrant.NewClient(qdrantServer.URL, "memory"), embeddings.NewClient(embedServer.URL), NewCache(time.Minute), "test", .97, .60, .90)
	srv.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func recallTextForSingleflightTest(t *testing.T, srv *Server, ctx context.Context, output chan<- string) {
	t.Helper()
	recallTextForSingleflightTestQuery(t, srv, ctx, "fact", output)
}

func recallTextForSingleflightTestQuery(t *testing.T, srv *Server, ctx context.Context, query string, output chan<- string) {
	t.Helper()
	result, err := srv.recallFacts(ctx, toolRequest(map[string]interface{}{"query": query, "limit": float64(1)}))
	if err != nil || result.IsError {
		t.Errorf("recall %q failed: %#v %v", query, result, err)
		output <- ""
		return
	}
	output <- result.StructuredContent.(RecallFactsResult).Facts[0].Text
}

func waitForRecallWaiters(t *testing.T, cache *Cache, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		cache.mu.RLock()
		flight := cache.inflight[key]
		got := 0
		if flight != nil {
			got = flight.waiters
		}
		cache.mu.RUnlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters=%d, want %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}
