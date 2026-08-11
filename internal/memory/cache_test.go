package memory

import (
	"context"
	"testing"
	"time"
)

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache(1 * time.Second)
	data := RecallFactsResult{Facts: []RecallFact{{Text: "hello"}}}

	c.SetRecall("key1", data)

	got, ok := c.GetRecall("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got.Facts) != 1 || got.Facts[0].Text != "hello" {
		t.Errorf("unexpected data: %v", got)
	}
}

func TestCache_Miss(t *testing.T) {
	c := NewCache(1 * time.Second)

	_, ok := c.GetRecall("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCache_Expiry(t *testing.T) {
	c := NewCache(10 * time.Millisecond)
	c.SetRecall("key1", RecallFactsResult{Facts: []RecallFact{{Text: "hello"}}})

	time.Sleep(20 * time.Millisecond)

	_, ok := c.GetRecall("key1")
	if ok {
		t.Error("expected cache miss after expiry")
	}
}

func TestCache_Invalidate(t *testing.T) {
	c := NewCache(1 * time.Minute)
	c.SetRecall("key1", RecallFactsResult{Facts: []RecallFact{{Text: "hello"}}})

	c.Invalidate()

	_, ok := c.GetRecall("key1")
	if ok {
		t.Error("expected cache miss after invalidate")
	}
}

func TestCache_AcquireRecallDoesNotHoldGlobalLockDuringHitUpdate(t *testing.T) {
	c := NewCache(time.Minute)
	c.SetRecall("blocked", RecallFactsResult{Facts: []RecallFact{{Text: "first"}}})
	c.SetRecall("independent", RecallFactsResult{Facts: []RecallFact{{Text: "second"}}})

	entered := make(chan struct{})
	release := make(chan struct{}, 1)
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()
	blockedDone := make(chan error, 1)
	go func() {
		_, _, err := c.AcquireRecall(context.Background(), "blocked", func(*RecallFactsResult) error {
			close(entered)
			<-release
			return nil
		})
		blockedDone <- err
	}()
	<-entered

	independentDone := make(chan error, 1)
	go func() {
		_, _, err := c.AcquireRecall(context.Background(), "independent", func(*RecallFactsResult) error {
			return nil
		})
		independentDone <- err
	}()
	select {
	case err := <-independentDone:
		if err != nil {
			t.Fatalf("independent cache hit failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("independent cache hit blocked behind another key's update")
	}

	release <- struct{}{}
	if err := <-blockedDone; err != nil {
		t.Fatalf("blocked cache hit failed: %v", err)
	}
}
