package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type recordingPurposeEmbedder struct {
	calls  []embeddings.Purpose
	inputs [][]string
	fail   error
}

func (embedder *recordingPurposeEmbedder) EmbedWithPurpose(context.Context, string,
	embeddings.Purpose, embeddings.InputProfile, string) ([]float32, error) {
	return []float32{1, 0}, embedder.fail
}

func (embedder *recordingPurposeEmbedder) EmbedBatchWithPurpose(_ context.Context, texts []string,
	purpose embeddings.Purpose, _ embeddings.InputProfile, _ string) ([][]float32, error) {
	embedder.calls = append(embedder.calls, purpose)
	embedder.inputs = append(embedder.inputs, append([]string(nil), texts...))
	if embedder.fail != nil {
		return nil, embedder.fail
	}
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestHybridRerankLiftsExactIdentifierAndPreservesCosine(t *testing.T) {
	cfg := Configuration{
		RetrievalStrategy: RetrievalHybridRRF, RRFConstant: 60,
		DenseCandidateLimit: 20,
	}
	points := []qdrant.Point{
		{ID: "semantic", Score: .99, Payload: map[string]any{"text": "general memory server"}},
		{ID: "exact", Score: .40, Payload: map[string]any{"text": "incident PM-1427"}},
	}
	ranked, err := rerankPoints("PM-1427", points, 2, cfg, "facts")
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ID != "exact" || ranked[0].Score != .40 {
		t.Fatalf("ranked = %#v, want exact first with original cosine", ranked)
	}
}

func TestHybridRerankFallsBackToDenseOrderWithoutLexicalSignal(t *testing.T) {
	cfg := Configuration{
		RetrievalStrategy: RetrievalHybridRRF, RRFConstant: 60,
		DenseCandidateLimit: 20,
	}
	points := []qdrant.Point{
		{ID: "second", Score: .3, Payload: map[string]any{"text": "other"}},
		{ID: "first", Score: .8, Payload: map[string]any{"text": "different"}},
	}
	ranked, err := rerankPoints("absent-token", points, 2, cfg, "facts")
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ID != "first" || ranked[1].ID != "second" {
		t.Fatalf("ranked IDs = %s, %s", ranked[0].ID, ranked[1].ID)
	}
}

func TestExperimentOverridesDoNotMutateDataset(t *testing.T) {
	original := &Dataset{
		SchemaVersion: CurrentDatasetSchemaVersion,
		Embedding: EmbeddingIdentity{
			Provider: "tei", ModelID: multilingualE5SmallModelID, ModelRevision: "revision",
			DType: "float32", Pooling: "mean", VectorSize: 2,
			InputProfile: LegacyRawV1, inputProfilePresent: true,
		},
		Configuration: Configuration{
			Name: "base", FactCollection: "facts", ChunkCollection: "chunks",
			FolderCollection: "folders", FolderTopK: 1, FolderThreshold: 0,
			TopK: []int{1}, RetrievalStrategy: RetrievalVectorOnly,
			present: map[string]bool{"retrieval_strategy": true, "dense_candidate_limit": true, "rrf_constant": true},
		},
	}
	beforeName := original.Configuration.Name
	beforeProfile := original.Embedding.InputProfile
	beforeStrategy := original.Configuration.RetrievalStrategy
	beforeTopK := append([]int(nil), original.Configuration.TopK...)
	name := "experiment"
	profile := MultilingualE5V1
	strategy := RetrievalHybridRRF
	limit, constant := 20, 60
	_, _ = WithExperimentOverrides(original, ExperimentOverrides{
		ConfigurationName: &name, InputProfile: &profile, RetrievalStrategy: &strategy,
		DenseCandidateLimit: &limit, RRFConstant: &constant,
	}, "live")
	if original.Configuration.Name != beforeName ||
		original.Embedding.InputProfile != beforeProfile ||
		original.Configuration.RetrievalStrategy != beforeStrategy ||
		len(original.Configuration.TopK) != len(beforeTopK) ||
		original.Configuration.TopK[0] != beforeTopK[0] {
		t.Fatal("experiment overrides mutated input dataset")
	}
}

func TestDurationSummaryDeterministic(t *testing.T) {
	got := summarizeDurations([]int64{40, 10, 20, 30})
	want := DurationSummary{Count: 4, Min: 10, P50: 20, P95: 40, Max: 40}
	if got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}

func TestTEIFixtureCorpusEmbeddingUsesPurposeOrderWithoutMutatingSource(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	cloned := cloneDataset(dataset)
	embedder := &recordingPurposeEmbedder{}
	count, err := embedFixtureCorpus(context.Background(), &cloned, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(dataset.Facts)+len(dataset.Chunks)+len(dataset.Folders) {
		t.Fatalf("embedded count = %d", count)
	}
	wantPurposes := []embeddings.Purpose{
		embeddings.FactPassage, embeddings.ChunkPassage, embeddings.FolderPassage,
	}
	for i, want := range wantPurposes {
		if embedder.calls[i] != want {
			t.Fatalf("purpose call %d = %v, want %v", i, embedder.calls[i], want)
		}
	}
	if dataset.Facts[1].Vector[0] != 0 || cloned.Facts[1].Vector[0] != 1 {
		t.Fatal("TEI fixture corpus preparation mutated the source or failed to replace clone vectors")
	}
}

func TestTEIFixtureCorpusEmbeddingRejectsMissingText(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts[0].Payload["text"] = 42
	if _, err := embedFixtureCorpus(context.Background(), dataset, &recordingPurposeEmbedder{}); err == nil ||
		!strings.Contains(err.Error(), `facts point "42"`) ||
		strings.Contains(err.Error(), "numeric") {
		t.Fatalf("missing corpus text error = %v", err)
	}
}

func TestV3DiagnosticsStrictDecodeAndComparisonIsInformational(t *testing.T) {
	baseline := validV3ComparisonReport()
	baseline.Diagnostics = &Diagnostics{Query: QueryDiagnostics{
		Total: DurationSummary{Count: 1, Min: 10, P50: 10, P95: 10, Max: 10},
	}}
	candidate := baseline
	candidate.Diagnostics = &Diagnostics{Query: QueryDiagnostics{
		Total: DurationSummary{Count: 1, Min: 100, P50: 100, P95: 100, Max: 100},
	}}
	comparison, err := Compare(baseline, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.BaselineDiagnostics == nil || comparison.CandidateDiagnostics == nil {
		t.Fatal("comparison omitted informational diagnostics")
	}

	invalid := baseline
	invalid.Diagnostics = &Diagnostics{Query: QueryDiagnostics{
		Total: DurationSummary{Count: 1, Min: 10, P50: 5, P95: 10, Max: 10},
	}}
	encoded, err := RenderJSON(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err == nil || !strings.Contains(err.Error(), "diagnostics") {
		t.Fatalf("DecodeReport() error = %v, want strict diagnostics rejection", err)
	}
}
