package eval

import (
	"context"
	"strings"
	"testing"
)

func TestRunRejectsUnsupportedV3ExperimentBeforeExternalCalls(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dataset)
		want   string
	}{
		{
			name: "input profile",
			mutate: func(dataset *Dataset) {
				dataset.Embedding.ModelID = multilingualE5SmallModelID
				dataset.Embedding.InputProfile = MultilingualE5V1
			},
			want: "input profile",
		},
		{
			name: "hybrid strategy",
			mutate: func(dataset *Dataset) {
				dataset.Configuration.RetrievalStrategy = RetrievalHybridRRF
				dataset.Configuration.DenseCandidateLimit = 20
				dataset.Configuration.RRFConstant = 60
			},
			want: "retrieval strategy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(dataset)
			embedCalled := false
			_, err = Run(context.Background(), dataset, RunOptions{
				Source: "live",
				Embed: func(context.Context, string) ([]float32, error) {
					embedCalled = true
					return nil, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) ||
				!strings.Contains(err.Error(), "not supported") {
				t.Fatalf("Run() error = %v, want safe unsupported %q error", err, tt.want)
			}
			if embedCalled {
				t.Fatal("unsupported v3 configuration reached embedder")
			}
		})
	}
}
