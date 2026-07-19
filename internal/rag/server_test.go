package rag

import "testing"

func TestParseSearchDocumentsArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{name: "defaults", args: map[string]any{"query": "memory"}},
		{name: "flat", args: map[string]any{"query": "memory", "limit": float64(100), "mode": "flat"}},
		{name: "blank query", args: map[string]any{"query": "  "}, wantErr: true},
		{name: "zero limit", args: map[string]any{"query": "memory", "limit": float64(0)}, wantErr: true},
		{name: "huge limit", args: map[string]any{"query": "memory", "limit": float64(101)}, wantErr: true},
		{name: "fractional limit", args: map[string]any{"query": "memory", "limit": 1.5}, wantErr: true},
		{name: "unknown mode", args: map[string]any{"query": "memory", "mode": "magic"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, gotErr := parseSearchDocumentsArgs(tt.args)
			if (gotErr != "") != tt.wantErr {
				t.Fatalf("error=%q, wantErr=%v", gotErr, tt.wantErr)
			}
		})
	}
}
