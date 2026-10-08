package factgrouping

import (
	"context"
	"errors"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type fakeStore struct {
	point  qdrant.Point
	writes int
	fail   bool
}

func (f *fakeStore) Get(context.Context, string) (qdrant.Point, bool, error) {
	return f.point, true, nil
}
func (f *fakeStore) SetGrouping(_ context.Context, _ string, tags []string, primary, ref string) error {
	f.writes++
	if f.fail {
		return errors.New("transport unknown")
	}
	f.point.Payload["tags"] = append([]string{}, tags...)
	f.point.Payload["primary_tag"] = primary
	f.point.Payload["ai_grouping_ref"] = ref
	return nil
}
func manifest(t *testing.T) (Manifest, *fakeStore) {
	p := map[string]interface{}{"text": "synthetic fact", "namespace": "projects", "tags": []string{"a"}, "primary_tag": "a", "lifecycle_state": "current", "recall_count": float64(2)}
	fp, _ := Fingerprint(p)
	m := Manifest{SchemaVersion: 1, ID: "reviewed-1", CatalogHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Approved: true, Changes: []Change{{PointID: "1", Fingerprint: fp, Before: Group{Tags: []string{"a"}, PrimaryTag: "a"}, After: Group{Tags: []string{"b"}, PrimaryTag: "b", AuditRef: "reviewed-1"}}}}
	return m, &fakeStore{point: qdrant.Point{ID: "1", Payload: p, Vector: []float32{0.1, 0.2}}}
}
func privateDir(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestApplyRollbackAndCounters(t *testing.T) {
	m, f := manifest(t)
	d := privateDir(t)
	vec := append([]float32{}, f.point.Vector...)
	if _, err := Run(context.Background(), f, m, filepath.Join(d, "apply.json"), false, false); err == nil {
		t.Fatal("must stop writers")
	}
	if _, err := Run(context.Background(), f, m, filepath.Join(d, "apply.json"), true, false); err != nil {
		t.Fatal(err)
	}
	f.point.Payload["recall_count"] = float64(9)
	if _, err := Run(context.Background(), f, m, filepath.Join(d, "apply.json"), true, false); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 {
		t.Fatal("duplicate write")
	}
	if _, err := Run(context.Background(), f, m, filepath.Join(d, "rollback.json"), true, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(vec, f.point.Vector) || f.point.Payload["text"] != "synthetic fact" || f.point.Payload["recall_count"] != float64(9) {
		t.Fatal("unrelated metadata changed")
	}
	if s, _ := os.Stat(filepath.Join(d, "apply.json")); s.Mode().Perm() != 0600 {
		t.Fatal("journal perms")
	}
}
func TestAmbiguousNeverRedispatches(t *testing.T) {
	m, f := manifest(t)
	f.fail = true
	j := filepath.Join(privateDir(t), "apply.json")
	if _, err := Run(context.Background(), f, m, j, true, false); err == nil {
		t.Fatal("ambiguous error")
	}
	f.fail = false
	if _, err := Run(context.Background(), f, m, j, true, false); err == nil {
		t.Fatal("unknown outcome must block")
	}
	if f.writes != 1 {
		t.Fatal("automatic retry")
	}
}
func TestChangedMetadataAndRollbackConflict(t *testing.T) {
	m, f := manifest(t)
	f.point.Payload["text"] = "changed"
	if _, err := Run(context.Background(), f, m, filepath.Join(privateDir(t), "j.json"), true, false); err == nil {
		t.Fatal("changed fact")
	}
	if f.writes != 0 {
		t.Fatal("must not overwrite")
	}
}
func TestCounterIndependentFingerprint(t *testing.T) {
	m, f := manifest(t)
	f.point.Payload["recall_count"] = float64(100)
	f.point.Payload["last_recalled_at"] = "later"
	fp, _ := Fingerprint(f.point.Payload)
	if fp != m.Changes[0].Fingerprint {
		t.Fatal("read counters cause review churn")
	}
}
