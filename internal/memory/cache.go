package memory

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type recallCacheEntry struct {
	mu        sync.Mutex
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
	recalls    map[string]*recallCacheEntry
	inflight   map[string]*recallFlight
	generation uint64
	ttl        time.Duration
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		recalls:  make(map[string]*recallCacheEntry),
		inflight: make(map[string]*recallFlight),
		ttl:      ttl,
	}
}

// AcquireRecall returns an atomically updated cache hit or a leader lease for
// the caller to populate. Followers wait for the current leader and retry.
func (c *Cache) AcquireRecall(ctx context.Context, key string, updateHit func(*RecallFactsResult) error) (RecallFactsResult, *recallFlight, error) {
	for {
		c.mu.Lock()
		if entry, ok := c.recalls[key]; ok {
			// Serialize only this key while updateHit may wait for queue space.
			c.mu.Unlock()
			entry.mu.Lock()
			c.mu.Lock()
			if c.recalls[key] != entry {
				c.mu.Unlock()
				entry.mu.Unlock()
				continue
			}
			if time.Since(entry.timestamp) > c.ttl {
				delete(c.recalls, key)
				c.mu.Unlock()
				entry.mu.Unlock()
				continue
			} else {
				c.mu.Unlock()
				working := cloneRecallFactsResult(entry.result)
				if err := updateHit(&working); err != nil {
					entry.mu.Unlock()
					return RecallFactsResult{}, nil, err
				}
				entry.result = cloneRecallFactsResult(working)
				entry.mu.Unlock()
				return working, nil, nil
			}
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
		c.recalls[key] = &recallCacheEntry{timestamp: time.Now(), result: cloneRecallFactsResult(*result)}
	}
	delete(c.inflight, key)
	close(flight.done)
	return published
}

func (c *Cache) GetRecall(key string) (RecallFactsResult, bool) {
	for {
		c.mu.RLock()
		entry, ok := c.recalls[key]
		c.mu.RUnlock()
		if !ok {
			return RecallFactsResult{}, false
		}
		entry.mu.Lock()
		c.mu.RLock()
		current := c.recalls[key] == entry
		c.mu.RUnlock()
		if !current {
			entry.mu.Unlock()
			continue
		}
		if time.Since(entry.timestamp) > c.ttl {
			entry.mu.Unlock()
			return RecallFactsResult{}, false
		}
		result := cloneRecallFactsResult(entry.result)
		entry.mu.Unlock()
		return result, true
	}
}

func (c *Cache) SetRecall(key string, result RecallFactsResult) {
	c.mu.Lock()
	previous := c.recalls[key]
	c.recalls[key] = &recallCacheEntry{
		timestamp: time.Now(),
		result:    cloneRecallFactsResult(result),
	}
	c.mu.Unlock()
	if previous != nil {
		previous.mu.Lock()
		previous.mu.Unlock()
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
	entries := make([]*recallCacheEntry, 0, len(c.recalls))
	for _, entry := range c.recalls {
		entries = append(entries, entry)
	}
	c.generation++
	c.recalls = make(map[string]*recallCacheEntry)
	for key, flight := range c.inflight {
		delete(c.inflight, key)
		close(flight.done)
	}
	c.mu.Unlock()
	// Let hit updates that started before invalidation finish before the
	// mutating request returns, without blocking unrelated cache keys.
	for _, entry := range entries {
		entry.mu.Lock()
		entry.mu.Unlock()
	}
}
