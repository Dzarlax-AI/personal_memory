package lifecyclemigration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const manifestVersion = 1

type Store interface {
	ScrollAll(ctx context.Context, filters map[string]interface{}, withVector bool) ([]qdrant.ScrollPoint, error)
	Get(ctx context.Context, id string) (qdrant.Point, bool, error)
	SetPayload(ctx context.Context, id string, payload map[string]interface{}) error
	ReplaceLifecyclePayload(ctx context.Context, id string, set map[string]interface{}, deleteKeys []string) error
}

type Options struct {
	Collection    string
	Apply         bool
	WritesStopped bool
	ManifestPath  string
	RollbackPath  string
}

type Report struct {
	Mode           string
	Scanned        int
	Planned        int
	Applied        int
	AlreadyApplied int
	RolledBack     int
	Conflicts      int
	Invalid        int
	PointIDs       []string
}

type fieldSnapshot struct {
	Present bool        `json:"present"`
	Value   interface{} `json:"value,omitempty"`
}

type manifestEntry struct {
	Type    string                   `json:"type"`
	PointID string                   `json:"point_id"`
	Before  map[string]fieldSnapshot `json:"before"`
	Applied map[string]interface{}   `json:"applied"`
}

type manifestHeader struct {
	Type         string `json:"type"`
	Version      int    `json:"version"`
	Collection   string `json:"collection"`
	PlanChecksum string `json:"plan_checksum"`
	CreatedAt    string `json:"created_at"`
	Scanned      int    `json:"scanned"`
	Invalid      int    `json:"invalid"`
}

var appliedTarget = map[string]interface{}{
	"lifecycle_state": "current",
	"canonical":       false,
	"supersedes":      []string{},
	"superseded_by":   []string{},
}

func Run(ctx context.Context, store Store, options Options) (Report, error) {
	if strings.TrimSpace(options.Collection) == "" {
		return Report{}, errors.New("collection is required")
	}
	if options.RollbackPath != "" {
		if options.Apply || options.ManifestPath != "" {
			return Report{}, errors.New("rollback cannot be combined with apply or manifest path")
		}
		if !options.WritesStopped {
			return Report{}, errors.New("rollback requires confirmation that writers are stopped")
		}
		return rollback(ctx, store, options.Collection, options.RollbackPath)
	}
	if !options.Apply {
		return dryRun(ctx, store)
	}
	if strings.TrimSpace(options.ManifestPath) == "" {
		return Report{}, errors.New("apply requires a rollback manifest path")
	}
	if !options.WritesStopped {
		return Report{}, errors.New("apply requires confirmation that writers are stopped")
	}
	return apply(ctx, store, options.Collection, options.ManifestPath)
}

func dryRun(ctx context.Context, store Store) (Report, error) {
	entries, scanned, invalid, err := scan(ctx, store)
	report := Report{Mode: "dry-run", Scanned: scanned, Planned: len(entries), Invalid: invalid}
	if err != nil {
		return report, err
	}
	report.PointIDs = entryIDs(entries)
	return report, nil
}

func apply(ctx context.Context, store Store, collection, manifestPath string) (Report, error) {
	header, entries, err := readManifest(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		var scanned, invalid int
		entries, scanned, invalid, err = scan(ctx, store)
		if err != nil {
			return Report{Mode: "apply"}, err
		}
		header = manifestHeader{
			Type:         "header",
			Version:      manifestVersion,
			Collection:   collection,
			PlanChecksum: checksumEntries(entries),
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
			Scanned:      scanned,
			Invalid:      invalid,
		}
		if err := createManifest(manifestPath, header, entries); err != nil {
			return Report{Mode: "apply"}, err
		}
	} else if err != nil {
		return Report{Mode: "apply"}, err
	}
	if header.Collection != collection {
		return Report{Mode: "apply"}, fmt.Errorf("manifest collection %q does not match %q", header.Collection, collection)
	}
	if header.PlanChecksum != checksumEntries(entries) {
		return Report{Mode: "apply"}, errors.New("manifest plan checksum mismatch")
	}
	if err := validateManifestEntries(entries); err != nil {
		return Report{Mode: "apply"}, err
	}

	report := Report{
		Mode:     "apply",
		Scanned:  header.Scanned,
		Planned:  len(entries),
		Invalid:  header.Invalid,
		PointIDs: entryIDs(entries),
	}
	for _, entry := range entries {
		point, found, err := store.Get(ctx, entry.PointID)
		if err != nil {
			return report, fmt.Errorf("get point %s before apply: %w", entry.PointID, err)
		}
		if !found {
			report.Conflicts++
			continue
		}
		if isLegacy(point.Payload) {
			if err := store.SetPayload(ctx, entry.PointID, cloneMap(entry.Applied)); err != nil {
				return report, fmt.Errorf("apply lifecycle to point %s: %w", entry.PointID, err)
			}
			report.Applied++
			continue
		}
		if lifecycleSubsetEqual(entry.PointID, point.Payload, entry.Applied) {
			report.AlreadyApplied++
			continue
		}
		report.Conflicts++
	}
	if report.Conflicts > 0 {
		return report, fmt.Errorf("apply completed with %d conflicts", report.Conflicts)
	}
	return report, nil
}

func rollback(ctx context.Context, store Store, collection, manifestPath string) (Report, error) {
	header, entries, err := readManifest(manifestPath)
	if err != nil {
		return Report{Mode: "rollback"}, err
	}
	if header.Collection != collection {
		return Report{Mode: "rollback"}, fmt.Errorf("manifest collection %q does not match %q", header.Collection, collection)
	}
	if header.PlanChecksum != checksumEntries(entries) {
		return Report{Mode: "rollback"}, errors.New("manifest plan checksum mismatch")
	}
	if err := validateManifestEntries(entries); err != nil {
		return Report{Mode: "rollback"}, err
	}
	report := Report{Mode: "rollback", Scanned: len(entries), Planned: len(entries), PointIDs: entryIDs(entries)}
	for _, entry := range entries {
		point, found, err := store.Get(ctx, entry.PointID)
		if err != nil {
			return report, fmt.Errorf("get point %s before rollback: %w", entry.PointID, err)
		}
		if !found || !lifecycleSubsetEqual(entry.PointID, point.Payload, entry.Applied) {
			report.Conflicts++
			continue
		}
		set, deleteKeys := restoreMutation(entry.Before)
		if err := store.ReplaceLifecyclePayload(ctx, entry.PointID, set, deleteKeys); err != nil {
			return report, fmt.Errorf("rollback lifecycle for point %s: %w", entry.PointID, err)
		}
		report.RolledBack++
	}
	if report.Conflicts > 0 {
		return report, fmt.Errorf("rollback completed with %d conflicts", report.Conflicts)
	}
	return report, nil
}

func scan(ctx context.Context, store Store) ([]manifestEntry, int, int, error) {
	points, err := store.ScrollAll(ctx, nil, false)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("scan memory points: %w", err)
	}
	entries := make([]manifestEntry, 0)
	invalid := 0
	for _, point := range points {
		if isLegacy(point.Payload) {
			entries = append(entries, manifestEntry{
				Type:    "point",
				PointID: point.ID,
				Before:  snapshotLifecycle(point.Payload),
				Applied: cloneMap(appliedTarget),
			})
			continue
		}
		if view, err := lifecycle.Parse(point.Payload, point.ID); err != nil || !view.Valid {
			invalid++
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].PointID < entries[j].PointID })
	return entries, len(points), invalid, nil
}

func isLegacy(payload map[string]interface{}) bool {
	for _, key := range lifecycle.PayloadFields() {
		if _, exists := payload[key]; exists {
			return false
		}
	}
	return true
}

func snapshotLifecycle(payload map[string]interface{}) map[string]fieldSnapshot {
	result := make(map[string]fieldSnapshot, len(lifecycle.PayloadFields()))
	for _, key := range lifecycle.PayloadFields() {
		value, present := payload[key]
		result[key] = fieldSnapshot{Present: present, Value: value}
	}
	return result
}

func restoreMutation(before map[string]fieldSnapshot) (map[string]interface{}, []string) {
	set := make(map[string]interface{})
	deleteKeys := make([]string, 0, len(before))
	for _, key := range lifecycle.PayloadFields() {
		snapshot := before[key]
		if snapshot.Present {
			set[key] = snapshot.Value
		} else {
			deleteKeys = append(deleteKeys, key)
		}
	}
	return set, deleteKeys
}

func lifecycleSubsetEqual(pointID string, payload, expected map[string]interface{}) bool {
	view, err := lifecycle.Parse(payload, pointID)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(normalizeJSON(lifecycle.PayloadFromView(view)), normalizeJSON(expected))
}

func normalizeJSON(value interface{}) interface{} {
	encoded, _ := json.Marshal(value)
	var normalized interface{}
	_ = json.Unmarshal(encoded, &normalized)
	return normalized
}

func checksumEntries(entries []manifestEntry) string {
	hash := sha256.New()
	for _, entry := range entries {
		encoded, _ := json.Marshal(entry)
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validateManifestEntries(entries []manifestEntry) error {
	seen := make(map[string]struct{}, len(entries))
	fields := lifecycle.PayloadFields()
	for _, entry := range entries {
		if _, duplicate := seen[entry.PointID]; duplicate {
			return fmt.Errorf("manifest contains duplicate point ID %s", entry.PointID)
		}
		seen[entry.PointID] = struct{}{}
		if !reflect.DeepEqual(normalizeJSON(entry.Applied), normalizeJSON(appliedTarget)) {
			return fmt.Errorf("manifest point %s has an unsupported applied target", entry.PointID)
		}
		if len(entry.Before) != len(fields) {
			return fmt.Errorf("manifest point %s has an incomplete before snapshot", entry.PointID)
		}
		for _, key := range fields {
			snapshot, exists := entry.Before[key]
			if !exists || snapshot.Present || snapshot.Value != nil {
				return fmt.Errorf("manifest point %s is not a legacy lifecycle migration", entry.PointID)
			}
		}
	}
	return nil
}

func createManifest(path string, header manifestHeader, entries []manifestEntry) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create rollback manifest: %w", err)
	}
	complete := false
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); returnErr == nil && closeErr != nil {
				returnErr = closeErr
			}
		}
		if !complete {
			_ = os.Remove(path)
		}
	}()
	writer := bufio.NewWriter(file)
	encoder := json.NewEncoder(writer)
	if err := encoder.Encode(header); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("write manifest entry: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush rollback manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync rollback manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close rollback manifest: %w", err)
	}
	closed = true
	complete = true
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open manifest directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync manifest directory: %w", err)
	}
	return nil
}

func readManifest(path string) (manifestHeader, []manifestEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return manifestHeader{}, nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return manifestHeader{}, nil, err
		}
		return manifestHeader{}, nil, errors.New("rollback manifest is empty")
	}
	var header manifestHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return manifestHeader{}, nil, fmt.Errorf("decode manifest header: %w", err)
	}
	if header.Type != "header" || header.Version != manifestVersion {
		return manifestHeader{}, nil, fmt.Errorf("unsupported manifest header type=%q version=%d", header.Type, header.Version)
	}
	var entries []manifestEntry
	for scanner.Scan() {
		var entry manifestEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return manifestHeader{}, nil, fmt.Errorf("decode manifest entry: %w", err)
		}
		if entry.Type != "point" || strings.TrimSpace(entry.PointID) == "" {
			return manifestHeader{}, nil, errors.New("invalid manifest point entry")
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return manifestHeader{}, nil, err
	}
	return header, entries, nil
}

func entryIDs(entries []manifestEntry) []string {
	ids := make([]string, len(entries))
	for i := range entries {
		ids[i] = entries[i].PointID
	}
	return ids
}

func cloneMap(source map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
