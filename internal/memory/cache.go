package memory

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type recallCacheEntry struct {
	timestamp time.Time
	result    RecallFactsResult
}

type recallFlight struct {
	done       chan struct{}
	generation uint64
	waiters    int
}

type Cache struct {
	mu         sync.RWMutex
	recalls    map[string]recallCacheEntry
	inflight   map[string]*recallFlight
	generation uint64
	ttl        time.Duration
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		recalls:  make(map[string]recallCacheEntry),
		inflight: make(map[string]*recallFlight),
		ttl:      ttl,
	}
}

// AcquireRecall returns an atomically updated cache hit or a leader lease for
// the caller to populate. Followers wait for the current leader and retry.
func (c *Cache) AcquireRecall(ctx context.Context, key string, updateHit func(*RecallFactsResult) error) (RecallFactsResult, *recallFlight, error) {
	for {
		c.mu.Lock()
		if entry, ok := c.recalls[key]; ok && time.Since(entry.timestamp) <= c.ttl {
			working := cloneRecallFactsResult(entry.result)
			if err := updateHit(&working); err != nil {
				c.mu.Unlock()
				return RecallFactsResult{}, nil, err
			}
			entry.result = cloneRecallFactsResult(working)
			c.recalls[key] = entry
			c.mu.Unlock()
			return working, nil, nil
		} else if ok {
			delete(c.recalls, key)
		}
		if flight := c.inflight[key]; flight != nil {
			flight.waiters++
			done := flight.done
			c.mu.Unlock()
			select {
			case <-done:
				c.mu.Lock()
				flight.waiters--
				c.mu.Unlock()
				continue
			case <-ctx.Done():
				c.mu.Lock()
				flight.waiters--
				c.mu.Unlock()
				return RecallFactsResult{}, nil, fmt.Errorf("wait for recall result: %w", ctx.Err())
			}
		}
		flight := &recallFlight{done: make(chan struct{}), generation: c.generation}
		c.inflight[key] = flight
		c.mu.Unlock()
		return RecallFactsResult{}, flight, nil
	}
}

// FinishRecall wakes followers and publishes result only when the lease still
// belongs to the current cache generation. A nil result aborts the flight.
func (c *Cache) FinishRecall(key string, flight *recallFlight, result *RecallFactsResult) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[key] != flight {
		return false
	}
	published := result != nil && flight.generation == c.generation
	if published {
		c.recalls[key] = recallCacheEntry{timestamp: time.Now(), result: cloneRecallFactsResult(*result)}
	}
	delete(c.inflight, key)
	close(flight.done)
	return published
}

func (c *Cache) GetRecall(key string) (RecallFactsResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.recalls[key]
	if !ok || time.Since(entry.timestamp) > c.ttl {
		return RecallFactsResult{}, false
	}
	return cloneRecallFactsResult(entry.result), true
}

func (c *Cache) SetRecall(key string, result RecallFactsResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recalls[key] = recallCacheEntry{
		timestamp: time.Now(),
		result:    cloneRecallFactsResult(result),
	}
}

func cloneRecallFactsResult(result RecallFactsResult) RecallFactsResult {
	cloned := result
	cloned.Facts = make([]RecallFact, len(result.Facts))
	for index, fact := range result.Facts {
		cloned.Facts[index] = fact
		cloned.Facts[index].Tags = append([]string{}, fact.Tags...)
		cloned.Facts[index].ReasonCodes = append([]LifecycleReasonCode{}, fact.ReasonCodes...)
		cloned.Facts[index].Lifecycle.Supersedes = append([]string{}, fact.Lifecycle.Supersedes...)
		cloned.Facts[index].Lifecycle.SupersededBy = append([]string{}, fact.Lifecycle.SupersededBy...)
		if fact.Lifecycle.Provenance != nil {
			provenance := *fact.Lifecycle.Provenance
			cloned.Facts[index].Lifecycle.Provenance = &provenance
		}
	}
	return cloned
}

func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.recalls = make(map[string]recallCacheEntry)
	for key, flight := range c.inflight {
		delete(c.inflight, key)
		close(flight.done)
	}
}
