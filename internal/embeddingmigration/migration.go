// Package embeddingmigration copies data into new model-bound collections.
// It never mutates its sources or changes the server's active configuration.
package embeddingmigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddingidentity"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/Dzarlax-AI/personal-memory/internal/rag"
)

const markerKey = "personal_memory.embedding_migration"

type Manifest struct {
	SchemaVersion         int    `json:"schema_version"`
	Role                  string `json:"role"`
	SourceCollection      string `json:"source_collection"`
	TargetCollection      string `json:"target_collection"`
	SourceSHA256          string `json:"source_sha256"`
	SelectedPayloadSHA256 string `json:"selected_payload_sha256"`
	SourceCount           int    `json:"source_count"`
	SelectedCount         int    `json:"selected_count"`
	SourceIdentity        any    `json:"source_identity"`
	TargetIdentity        any    `json:"target_identity"`
	Dimensions            int    `json:"dimensions"`
}

type Job struct {
	Source, Target *qdrant.Client
	Backend        *embeddings.Backend
	Config         embeddings.BackendConfig
	Role           string
}

func fingerprint(points []qdrant.ScrollPoint, vector bool) (string, error) {
	ordered := append([]qdrant.ScrollPoint(nil), points...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	hash := sha256.New()
	seen := map[string]bool{}
	for _, p := range ordered {
		if p.ID == "" || seen[p.ID] {
			return "", errors.New("missing or duplicate migration point identity")
		}
		seen[p.ID] = true
		record := struct {
			ID      string
			Payload map[string]any
			Vector  []float32
		}{ID: p.ID, Payload: p.Payload}
		if vector {
			record.Vector = p.Vector
		}
		if err := json.NewEncoder(hash).Encode(record); err != nil {
			return "", errors.New("cannot fingerprint migration data")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func purpose(role string) (embeddings.Purpose, error) {
	switch role {
	case "memory":
		return embeddings.FactPassage, nil
	case "chunks":
		return embeddings.ChunkPassage, nil
	case "folders":
		return embeddings.FolderPassage, nil
	}
	return 0, errors.New("migration role must be memory, chunks or folders")
}
func (j Job) read(ctx context.Context) ([]qdrant.ScrollPoint, []qdrant.ScrollPoint, error) {
	all, err := readPoints(ctx, j.Source, 0)
	if err != nil {
		return nil, nil, errors.New("cannot read migration source")
	}
	selected := all
	if j.Role == "chunks" {
		selected, err = rag.MigrationChunks(ctx, j.Source, all)
		if err != nil {
			return nil, nil, err
		}
	}
	for _, p := range selected {
		if text, ok := p.Payload[j.textField()].(string); !ok || strings.TrimSpace(text) == "" {
			return nil, nil, errors.New("migration point has no nonempty embedding text")
		}
	}
	return all, selected, nil
}

func (j Job) Prepare(ctx context.Context) (Manifest, error) {
	if j.Source == nil || j.Target == nil || j.Source.CollectionName() == j.Target.CollectionName() {
		return Manifest{}, errors.New("migration requires distinct source and target collections")
	}
	if !collectionPattern.MatchString(j.Source.CollectionName()) || !collectionPattern.MatchString(j.Target.CollectionName()) {
		return Manifest{}, errors.New("invalid migration collection name")
	}
	if _, err := purpose(j.Role); err != nil {
		return Manifest{}, err
	}
	info, err := j.Source.CollectionInfo(ctx)
	if err != nil || !info.Exists {
		return Manifest{}, errors.New("migration source missing or unavailable")
	}
	identity, present := info.Metadata[embeddingidentity.MetadataKey]
	if !present {
		return Manifest{}, errors.New("migration source requires existing embedding identity")
	}
	if err := embeddingidentity.ValidateStoredIdentity(identity, info.VectorSize); err != nil {
		return Manifest{}, err
	}
	target, dimensions, err := embeddingidentity.BindingRecord(ctx, j.Backend, j.Config)
	if err != nil {
		return Manifest{}, err
	}
	all, selected, err := j.read(ctx)
	if err != nil {
		return Manifest{}, err
	}
	sourceCount, countErr := j.Source.ExactCount(ctx)
	if countErr != nil || sourceCount != uint64(len(all)) {
		return Manifest{}, errors.New("migration source count does not match complete scan")
	}
	sourceHash, err := fingerprint(all, false)
	if err != nil {
		return Manifest{}, err
	}
	payloadHash, err := fingerprint(selected, false)
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{1, j.Role, j.Source.CollectionName(), j.Target.CollectionName(), sourceHash, payloadHash, len(all), len(selected), identity, target, dimensions}, nil
}

func ManifestID(m Manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func WriteManifest(path string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("manifest destination must be a new private file")
	}
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	linkInfo, statErr := os.Lstat(path)
	if statErr != nil || !linkInfo.Mode().IsRegular() {
		return m, errors.New("manifest must not be a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return m, errors.New("cannot open migration manifest")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(linkInfo, info) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return m, errors.New("manifest must be a bounded private regular file")
	}
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || m.SchemaVersion != 1 {
		return m, errors.New("invalid migration manifest")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return m, errors.New("migration manifest has trailing data")
	}
	return m, nil
}
func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var x, y any
	dx := json.NewDecoder(bytes.NewReader(left))
	dx.UseNumber()
	dy := json.NewDecoder(bytes.NewReader(right))
	dy.UseNumber()
	if dx.Decode(&x) != nil || dy.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Apply requires stopped writers even on resume. Existing target contents must
// carry exactly this manifest's ownership marker. A failure leaves that isolated
// collection for verified resume; source and active server remain unchanged.
func (j Job) Apply(ctx context.Context, manifest Manifest, writersStopped bool) error {
	if !writersStopped {
		return errors.New("apply requires explicit confirmation that all source and target writers are stopped")
	}
	current, err := j.Prepare(ctx)
	if err != nil {
		return err
	}
	if !sameJSON(current, manifest) {
		return errors.New("migration manifest is stale or does not match this job")
	}
	id, err := ManifestID(manifest)
	if err != nil {
		return err
	}
	info, err := j.Target.CollectionInfo(ctx)
	if err != nil {
		return errors.New("cannot inspect target")
	}
	if info.Exists {
		if info.Metadata[markerKey] != id || !sameJSON(info.Metadata[embeddingidentity.MetadataKey], manifest.TargetIdentity) || info.VectorSize != manifest.Dimensions {
			return errors.New("target is not owned by this migration manifest")
		}
	} else if err := j.Target.CreateCollection(ctx, manifest.Dimensions, map[string]any{embeddingidentity.MetadataKey: manifest.TargetIdentity, markerKey: id}); err != nil {
		return errors.New("cannot create isolated migration target")
	}
	_, points, err := j.read(ctx)
	if err != nil {
		return err
	}

	existing, err := readPoints(ctx, j.Target, manifest.Dimensions)
	if err != nil {
		return errors.New("cannot inspect resume target")
	}
	expected := map[string]qdrant.ScrollPoint{}
	for _, point := range points {
		expected[point.ID] = point
	}
	completed := map[string]bool{}
	for _, point := range existing {
		source, ok := expected[point.ID]
		if !ok || !sameJSON(source.Payload, point.Payload) {
			return errors.New("resume target has unexpected or mismatched points")
		}
		completed[point.ID] = true
	}
	p, _ := purpose(j.Role)
	// Batch one bounds memory/context pressure for the local Gemma deployment.
	for _, point := range points {
		if completed[point.ID] {
			continue
		}
		vector, err := j.Backend.EmbedPurpose(ctx, point.Payload[j.textField()].(string), p)
		if err != nil {
			return err
		}
		if err = j.Target.Upsert(ctx, qdrant.Point{ID: point.ID, Payload: point.Payload, Vector: vector}); err != nil {
			return errors.New("migration target write failed; verify and resume same manifest")
		}
	}
	return j.Verify(ctx, manifest)
}

func (j Job) Verify(ctx context.Context, manifest Manifest) error {
	current, err := j.Prepare(ctx)
	if err != nil {
		return err
	}
	if !sameJSON(current, manifest) {
		return errors.New("migration source changed; target cannot be accepted")
	}
	id, err := ManifestID(manifest)
	if err != nil {
		return err
	}
	info, err := j.Target.CollectionInfo(ctx)
	if err != nil || !info.Exists || info.VectorSize != manifest.Dimensions || info.Metadata[markerKey] != id || !sameJSON(info.Metadata[embeddingidentity.MetadataKey], manifest.TargetIdentity) {
		return errors.New("migration target identity or ownership mismatch")
	}
	points, err := readPoints(ctx, j.Target, manifest.Dimensions)
	if err != nil {
		return errors.New("cannot verify migration target")
	}
	exactCount, countErr := j.Target.ExactCount(ctx)
	if countErr != nil || exactCount != uint64(len(points)) {
		return errors.New("migration target count does not match complete scan")
	}
	digest, err := fingerprint(points, false)
	if err != nil {
		return err
	}
	if len(points) != manifest.SelectedCount || digest != manifest.SelectedPayloadSHA256 {
		return errors.New("migration target count or payload mismatch")
	}
	// Check freshness once more after target scanning, including recall metadata.
	final, err := j.Prepare(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(final, current) {
		return errors.New("migration source changed during verification")
	}
	return nil
}

func (m Manifest) SafeSummary() string {
	return fmt.Sprintf("role=%s source_points=%d selected_points=%d excluded_generations_points=%d", m.Role, m.SourceCount, m.SelectedCount, m.SourceCount-m.SelectedCount)
}

func (j Job) textField() string {
	if j.Role == "folders" {
		return "summary"
	}
	return "text"
}

var collectionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Bound migration working sets independently of production corpus size.
// Exceeding a cap is a refusal, never a partial-corpus acceptance.
func readPoints(ctx context.Context, source *qdrant.Client, vectorDimensions int) ([]qdrant.ScrollPoint, error) {
	var points []qdrant.ScrollPoint
	var offset any
	seenOffsets := map[string]bool{}
	bytesRead := 0
	for {
		page, err := source.ScrollForMigration(ctx, 128, offset, vectorDimensions > 0)
		if err != nil {
			return nil, errors.New("migration scan failed")
		}
		for _, point := range page.Points {
			if vectorDimensions > 0 {
				if len(point.Vector) != vectorDimensions {
					return nil, errors.New("migration vector dimension mismatch")
				}
				norm := 0.0
				for _, value := range point.Vector {
					if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
						return nil, errors.New("migration vector is nonfinite")
					}
					norm += float64(value) * float64(value)
				}
				if norm == 0 {
					return nil, errors.New("migration vector is zero")
				}
			}
			// Source vectors do not participate in re-embedding. Target vectors are
			// verified per page, then released; only IDs and exact payloads are retained.
			point.Vector = nil
			encoded, err := json.Marshal(point.Payload)
			if err != nil {
				return nil, errors.New("migration payload cannot be encoded")
			}
			bytesRead += len(encoded) + len(point.ID) + len(point.Vector)*4
			if bytesRead > 128<<20 || len(points) >= 100000 {
				return nil, errors.New("migration scan exceeds bounded working set")
			}
			points = append(points, point)
		}
		if page.RawOffset == nil {
			break
		}
		encoded, _ := json.Marshal(page.RawOffset)
		key := string(encoded)
		if len(page.Points) == 0 || seenOffsets[key] {
			return nil, errors.New("migration pagination did not advance")
		}
		seenOffsets[key] = true
		offset = page.RawOffset
	}
	return points, nil
}
