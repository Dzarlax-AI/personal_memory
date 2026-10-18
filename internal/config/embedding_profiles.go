package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
)

// EmbeddingProfiles is opt-in. Secrets are referenced by environment variable
// name; only active backend construction reads them.
type EmbeddingProfiles struct {
	SchemaVersion int                                 `json:"schema_version"`
	Profiles      map[string]embeddings.BackendConfig `json:"profiles"`
	Bindings      EmbeddingBindings                   `json:"bindings"`
}
type EmbeddingBindings struct {
	Memory MemoryEmbeddingBinding `json:"memory"`
	RAG    RAGEmbeddingBinding    `json:"rag"`
}
type MemoryEmbeddingBinding struct {
	Profile    string               `json:"profile"`
	Collection string               `json:"collection"`
	Thresholds *EmbeddingThresholds `json:"thresholds,omitempty"`
}
type EmbeddingThresholds struct {
	Dedup    float64 `json:"dedup"`
	Related  float64 `json:"related"`
	Mutation float64 `json:"mutation"`
	Viz      float64 `json:"viz"`
	Basis    string  `json:"basis"`
}
type RAGEmbeddingBinding struct {
	Profile           string   `json:"profile"`
	ChunksCollection  string   `json:"chunks_collection"`
	FoldersCollection string   `json:"folders_collection"`
	SearchMode        string   `json:"search_mode"`
	FolderThreshold   *float64 `json:"folder_threshold,omitempty"`
}

var collectionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func LoadEmbeddingProfiles(path string) (*EmbeddingProfiles, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read EMBED_PROFILES_FILE")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return nil, errors.New("embedding profiles file exceeds limit or is unreadable")
	}
	return ParseEmbeddingProfiles(b)
}
func ParseEmbeddingProfiles(data []byte) (*EmbeddingProfiles, error) {
	// encoding/json accepts duplicate keys by default. Reject them at every depth.
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSONValue(d); err != nil {
		return nil, errors.New("invalid embedding profiles JSON or duplicate field")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("embedding profiles must contain one JSON object")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var p EmbeddingProfiles
	if err := d.Decode(&p); err != nil {
		return nil, errors.New("invalid embedding profiles schema")
	}
	if p.SchemaVersion != 1 || len(p.Profiles) == 0 || len(p.Profiles) > 32 {
		return nil, errors.New("unsupported embedding profiles version or profile count")
	}
	for name, profile := range p.Profiles {
		if !collectionNamePattern.MatchString(name) {
			return nil, errors.New("invalid embedding profile name")
		}
		if profile.InputProfile == "" {
			return nil, errors.New("embedding profile requires explicit input_profile")
		}
		if err := embeddings.ValidateBackendConfig(profile); err != nil {
			return nil, fmt.Errorf("embedding profile %s: %w", name, err)
		}
	}
	return &p, nil
}
func uniqueJSONValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errors.New("duplicate key")
			}
			seen[key] = true
			if err := uniqueJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid delimiter")
	}
	_, err = d.Token()
	return err
}

func (c *Config) loadEmbeddingProfiles() error {
	path := os.Getenv("EMBED_PROFILES_FILE")
	if path == "" {
		return nil
	}
	p, err := LoadEmbeddingProfiles(path)
	if err != nil {
		return err
	}
	c.EmbeddingProfiles = p
	c.MemoryCollection = p.Bindings.Memory.Collection
	c.RAGCollectionChunks = p.Bindings.RAG.ChunksCollection
	c.RAGCollectionFolders = p.Bindings.RAG.FoldersCollection
	c.RAGSearchMode = p.Bindings.RAG.SearchMode
	if t := p.Bindings.Memory.Thresholds; t != nil {
		c.DedupThreshold = t.Dedup
		c.RelatedFactLow = t.Related
		c.MutationMatchThreshold = t.Mutation
		c.VizSimilarityThreshold = t.Viz
	}
	if t := p.Bindings.RAG.FolderThreshold; t != nil {
		c.RAGFolderThreshold = *t
	}
	return nil
}
func (c *Config) validateEmbeddingBindings(indexerOnly bool) error {
	p := c.EmbeddingProfiles
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	check := func(profile string, names ...string) error {
		if _, ok := p.Profiles[profile]; !ok {
			return errors.New("embedding binding references missing profile")
		}
		for _, name := range names {
			if !collectionNamePattern.MatchString(name) || seen[name] {
				return errors.New("embedding collections must be distinct valid names")
			}
			seen[name] = true
		}
		return nil
	}
	b := p.Bindings.Memory
	if err := check(b.Profile, b.Collection); err != nil {
		return err
	}
	if !indexerOnly {
		profile := p.Profiles[b.Profile]
		if profile.Provider != "tei" || profile.InputProfile != embeddings.LegacyRawV1 {
			t := b.Thresholds
			if t == nil || strings.TrimSpace(t.Basis) == "" || len(t.Basis) > 512 {
				return errors.New("nonlegacy memory binding requires explicit thresholds and calibration basis")
			}
			for _, v := range []float64{t.Dedup, t.Related, t.Mutation, t.Viz} {
				if err := validateThreshold("embedding binding threshold", v); err != nil {
					return err
				}
			}
			if t.Related >= t.Dedup {
				return errors.New("embedding related threshold must be below dedup")
			}
		}
	}
	if c.EnableRAG {
		b := p.Bindings.RAG
		if err := check(b.Profile, b.ChunksCollection, b.FoldersCollection); err != nil {
			return err
		}
		if b.SearchMode != "flat" && b.SearchMode != "hierarchical" {
			return errors.New("RAG binding requires explicit search_mode")
		}
		profile := p.Profiles[b.Profile]
		if b.SearchMode == "hierarchical" && (profile.Provider != "tei" || profile.InputProfile != embeddings.LegacyRawV1) && b.FolderThreshold == nil {
			return errors.New("nonlegacy hierarchical binding requires folder_threshold")
		}
	}
	return nil
}

func (c *Config) MemoryCollectionName() string {
	if c.MemoryCollection == "" {
		return "memory"
	}
	return c.MemoryCollection
}

// MemoryCollectionFromEnvironment lets standalone maintenance tools follow the
// active collection without constructing an embedding client or reading keys.
func MemoryCollectionFromEnvironment() (string, error) {
	path := os.Getenv("EMBED_PROFILES_FILE")
	if path == "" {
		return "memory", nil
	}
	p, err := LoadEmbeddingProfiles(path)
	if err != nil {
		return "", err
	}
	binding := p.Bindings.Memory
	if !collectionNamePattern.MatchString(binding.Collection) {
		return "", errors.New("invalid active memory collection")
	}
	if _, ok := p.Profiles[binding.Profile]; !ok {
		return "", errors.New("active memory binding references missing profile")
	}
	return binding.Collection, nil
}
