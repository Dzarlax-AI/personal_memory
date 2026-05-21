package qdrant

import "testing"

func TestQdrantPointID_NumericString(t *testing.T) {
	got := qdrantPointID("12345")
	if got != int64(12345) {
		t.Fatalf("qdrantPointID numeric = %#v, want int64(12345)", got)
	}
}

func TestQdrantPointID_UUIDString(t *testing.T) {
	id := "4f08ef2a-42c0-45df-a6c3-5ca86db4ddf8"
	got := qdrantPointID(id)
	if got != id {
		t.Fatalf("qdrantPointID uuid = %#v, want %q", got, id)
	}
}
