package aimaintenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

const maxProjectionCacheBytes = 512 << 10

type catalogProjectionRecord struct {
	Namespace  string               `json:"namespace"`
	Tag        string               `json:"tag"`
	Original   contextcatalog.Entry `json:"original"`
	LiveHashes []string             `json:"live_hashes"`
}

type catalogProjectionCache struct {
	SchemaVersion int                       `json:"schema_version"`
	Entries       []catalogProjectionRecord `json:"entries"`
	Checksum      string                    `json:"checksum"`
}

func (c catalogProjectionCache) payload() any {
	return struct {
		SchemaVersion int                       `json:"schema_version"`
		Entries       []catalogProjectionRecord `json:"entries"`
	}{c.SchemaVersion, c.Entries}
}

func (c catalogProjectionCache) find(key string) (catalogProjectionRecord, bool) {
	for _, record := range c.Entries {
		if record.Namespace+"/"+record.Tag == key {
			return record, true
		}
	}
	return catalogProjectionRecord{}, false
}

func (w *catalogWorker) loadProjectionCache() (catalogProjectionCache, error) {
	cache := catalogProjectionCache{SchemaVersion: 1, Entries: []catalogProjectionRecord{}}
	if _, err := os.Lstat(w.projectionPath); os.IsNotExist(err) {
		return cache, nil
	} else if err != nil {
		return catalogProjectionCache{}, errors.New("private catalog source projections are invalid")
	}
	raw, err := aipolicy.ReadPrivate(w.projectionPath, maxProjectionCacheBytes)
	if err != nil {
		return catalogProjectionCache{}, errors.New("private catalog source projections are invalid")
	}
	if decodeProjectionCache(raw, &cache) != nil {
		return catalogProjectionCache{}, errors.New("private catalog source projections are invalid")
	}
	return cache, nil
}

func decodeProjectionCache(raw []byte, out *catalogProjectionCache) error {
	if len(raw) == 0 || len(raw) > maxProjectionCacheBytes {
		return errors.New("projection cache size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("projection cache has trailing data")
	}
	if out.SchemaVersion != 1 || len(out.Entries) > contextcatalog.MaxEntries || len(out.Checksum) != 64 {
		return errors.New("projection cache schema is invalid")
	}
	checksumBytes, err := hex.DecodeString(out.Checksum)
	if err != nil || len(checksumBytes) != sha256.Size {
		return errors.New("projection cache checksum is invalid")
	}
	payload, err := json.Marshal(out.payload())
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != out.Checksum {
		return errors.New("projection cache checksum mismatch")
	}
	seen := map[string]bool{}
	for _, record := range out.Entries {
		key := record.Namespace + "/" + record.Tag
		if record.Namespace != "projects" || record.Tag == "" || seen[key] || record.Original.Namespace != record.Namespace || record.Original.Tag != record.Tag || record.Original.DescriptionSource == "model_derived" || len(record.LiveHashes) == 0 || len(record.LiveHashes) > 2 {
			return errors.New("projection cache record is invalid")
		}
		seen[key] = true
		if err := contextcatalog.Validate(contextcatalog.Snapshot{SchemaVersion: contextcatalog.SchemaVersion, Version: "projection-cache", CreatedAt: record.OriginalCreatedAt(), Entries: []contextcatalog.Entry{record.Original}}); err != nil {
			return errors.New("projection cache source entry is invalid")
		}
		for _, digest := range record.LiveHashes {
			b, err := hex.DecodeString(digest)
			if err != nil || len(b) != sha256.Size {
				return errors.New("projection cache entry hash is invalid")
			}
		}
	}
	return nil
}

// OriginalCreatedAt returns a fixed valid timestamp used only to validate an
// individual cached entry; original snapshot metadata is not needed for a card.
func (r catalogProjectionRecord) OriginalCreatedAt() string { return "2026-01-01T00:00:00Z" }

func (w *catalogWorker) saveProjectionCache(cache catalogProjectionCache) error {
	sort.Slice(cache.Entries, func(i, j int) bool {
		return cache.Entries[i].Namespace+"/"+cache.Entries[i].Tag < cache.Entries[j].Namespace+"/"+cache.Entries[j].Tag
	})
	cache.SchemaVersion = 1
	cache.Checksum = ""
	payload, err := json.Marshal(cache.payload())
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	cache.Checksum = hex.EncodeToString(sum[:])
	raw, err := json.Marshal(cache)
	if err != nil || len(raw) > maxProjectionCacheBytes {
		return errors.New("projection cache exceeds limit")
	}
	return aipolicy.AtomicWrite(filepath.Dir(w.projectionPath), filepath.Base(w.projectionPath), raw)
}

func cacheRecordMatchesLive(record catalogProjectionRecord, live contextcatalog.Entry) bool {
	if !sameCatalogSourceIdentity(record.Original, live) {
		return false
	}
	hash, err := catalogEntryHash(live)
	if err != nil {
		return false
	}
	for _, expected := range record.LiveHashes {
		if hash == expected {
			return true
		}
	}
	return false
}

func sameCatalogSourceIdentity(original, live contextcatalog.Entry) bool {
	declaredMatches := original.DeclaredSummary == live.DeclaredSummary || (live.DescriptionSource == "model_derived" && original.DeclaredSummary == "" && live.DeclaredSummary == original.Summary)
	provenanceMatches := live.DescriptionSource == original.DescriptionSource || live.DescriptionSource == "model_derived"
	return original.Namespace == live.Namespace && original.Tag == live.Tag && original.Name == live.Name && original.ProjectID == live.ProjectID && original.ProjectKey == live.ProjectKey && original.Owner == live.Owner && original.ReviewStatus == live.ReviewStatus && declaredMatches && provenanceMatches && evidenceSlicesEqual(original.ClientEvidence, live.ClientEvidence)
}

func evidenceSlicesEqual(a, b []contextcatalog.Evidence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (w *catalogWorker) importReceiptProjections(current contextcatalog.Snapshot, cache catalogProjectionCache) (catalogProjectionCache, bool, error) {
	active := make(map[string]contextcatalog.Entry, len(current.Entries))
	for _, entry := range current.Entries {
		if entry.Namespace == "projects" && contextcatalog.Eligible(entry) && w.cfg.Egress.Allows(entry.Namespace, []string{entry.Tag}) {
			active[cardKey(entry)] = entry
		}
	}
	oldCount := len(cache.Entries)
	filtered := cache.Entries[:0]
	for _, record := range cache.Entries {
		if _, ok := active[record.Namespace+"/"+record.Tag]; ok {
			filtered = append(filtered, record)
		}
	}
	cache.Entries = filtered
	if len(cache.Entries) > len(active) {
		return cache, false, errors.New("projection cache exceeds eligible catalog")
	}
	byKey := map[string]int{}
	for i, record := range cache.Entries {
		byKey[record.Namespace+"/"+record.Tag] = i
	}
	changed := len(filtered) != oldCount
	files, err := os.ReadDir(filepath.Join(w.proposalWorker.dir, "proposals"))
	if os.IsNotExist(err) {
		return cache, changed, nil
	}
	if err != nil || len(files) > maxProposalFiles {
		return cache, false, errors.New("cannot safely migrate catalog source projections")
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		raw, err := aipolicy.ReadPrivate(filepath.Join(w.proposalWorker.dir, "proposals", file.Name()), MaxResponseBytes*4)
		if err != nil || !proposalFilenameMatches(file.Name(), raw) {
			continue
		}
		var p privateProposal
		if json.Unmarshal(raw, &p) != nil || p.SchemaVersion != 1 || p.CatalogOriginalAll == nil || len(p.CatalogPublished) == 0 || len(p.CatalogPublished) > contextcatalog.MaxEntries || contextcatalog.Validate(*p.CatalogOriginalAll) != nil {
			continue
		}
		containsGeneratedOriginal := false
		for _, entry := range p.CatalogOriginalAll.Entries {
			if entry.DescriptionSource == "model_derived" {
				containsGeneratedOriginal = true
				break
			}
		}
		if containsGeneratedOriginal {
			continue
		}
		for _, published := range p.CatalogPublished {
			key := published.Namespace + "/" + published.Tag
			live, ok := active[key]
			if !ok {
				continue
			}
			liveHash, err := catalogEntryHash(live)
			if err != nil || liveHash != published.EntryHash {
				continue
			}
			var original *contextcatalog.Entry
			for i := range p.CatalogOriginalAll.Entries {
				if cardKey(p.CatalogOriginalAll.Entries[i]) == key {
					original = &p.CatalogOriginalAll.Entries[i]
					break
				}
			}
			if original == nil || !sameCatalogSourceIdentity(*original, live) {
				continue
			}
			if idx, ok := byKey[key]; ok {
				if !reflect.DeepEqual(cache.Entries[idx].Original, *original) {
					continue
				}
				if !containsString(cache.Entries[idx].LiveHashes, liveHash) {
					cache.Entries[idx].LiveHashes = append(cache.Entries[idx].LiveHashes, liveHash)
					if len(cache.Entries[idx].LiveHashes) > 2 {
						cache.Entries[idx].LiveHashes = cache.Entries[idx].LiveHashes[len(cache.Entries[idx].LiveHashes)-2:]
					}
					changed = true
				}
				continue
			}
			cache.Entries = append(cache.Entries, catalogProjectionRecord{Namespace: published.Namespace, Tag: published.Tag, Original: *original, LiveHashes: []string{liveHash}})
			byKey[key] = len(cache.Entries) - 1
			changed = true
		}
	}
	if len(cache.Entries) > contextcatalog.MaxEntries {
		return cache, false, errors.New("projection cache exceeds catalog limit")
	}
	return cache, changed, nil
}

func (w *catalogWorker) persistOriginalProjections(current contextcatalog.Snapshot, input Input, published []privateCatalogPublished) error {
	cache, err := w.loadProjectionCache()
	if err != nil {
		return err
	}
	active := make(map[string]contextcatalog.Entry, len(current.Entries))
	for _, entry := range current.Entries {
		if entry.Namespace == "projects" && contextcatalog.Eligible(entry) && w.cfg.Egress.Allows(entry.Namespace, []string{entry.Tag}) {
			active[cardKey(entry)] = entry
		}
	}
	kept := cache.Entries[:0]
	for _, record := range cache.Entries {
		if _, ok := active[record.Namespace+"/"+record.Tag]; ok {
			kept = append(kept, record)
		}
	}
	cache.Entries = kept
	for _, expected := range published {
		key := expected.Namespace + "/" + expected.Tag
		live, ok := active[key]
		if !ok {
			return errors.New("published source card is not eligible")
		}
		var original *contextcatalog.Entry
		for i := range input.contextEntries {
			if cardKey(input.contextEntries[i]) == key {
				original = &input.contextEntries[i]
				break
			}
		}
		if original == nil || original.DescriptionSource == "model_derived" {
			return errors.New("original source card is unavailable")
		}
		liveHash, err := catalogEntryHash(live)
		if err != nil {
			return err
		}
		var record *catalogProjectionRecord
		for i := range cache.Entries {
			if cache.Entries[i].Namespace+"/"+cache.Entries[i].Tag == key {
				record = &cache.Entries[i]
				break
			}
		}
		if record != nil {
			if !cacheRecordMatchesLive(*record, live) {
				if live.DescriptionSource == "model_derived" || !sameCatalogSourceIdentity(*original, live) {
					return errors.New("current catalog source projection changed")
				}
				record.Original = *original
				record.LiveHashes = []string{liveHash}
			} else if !reflect.DeepEqual(record.Original, *original) {
				return errors.New("current catalog source projection changed")
			} else {
				record.LiveHashes = []string{liveHash}
			}
		} else {
			if live.DescriptionSource == "model_derived" || !sameCatalogSourceIdentity(*original, live) {
				return errors.New("current catalog source projection is unavailable")
			}
			cache.Entries = append(cache.Entries, catalogProjectionRecord{Namespace: expected.Namespace, Tag: expected.Tag, Original: *original, LiveHashes: []string{liveHash}})
			record = &cache.Entries[len(cache.Entries)-1]
		}
		if !containsString(record.LiveHashes, expected.EntryHash) {
			record.LiveHashes = append(record.LiveHashes, expected.EntryHash)
		}
		if len(record.LiveHashes) > 2 {
			return errors.New("catalog source projection hash bound exceeded")
		}
	}
	return w.saveProjectionCache(cache)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
