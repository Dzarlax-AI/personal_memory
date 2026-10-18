package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testProfiles = `{"schema_version":1,"profiles":{"gemma":{"provider":"llamacpp","model":"embeddinggemma-2","input_profile":"embeddinggemma-search-v1","dimensions":768,"url":"http://memory-gemma:8080","weights_sha256":"2188ac1deca4b77dffefd603c2776a9d76d9d74ec01841392982ebb840b09135"},"cloud":{"provider":"openai","model":"text-embedding-3-small","input_profile":"openai-text-v1","dimensions":1536,"api_key_env":"UNSET_TEST_EMBEDDING_KEY"}},"bindings":{"memory":{"profile":"gemma","collection":"memory_gemma","thresholds":{"dedup":0.98,"related":0.7,"mutation":0.95,"viz":0.7,"basis":"synthetic test values; not production calibration"}},"rag":{"profile":"gemma","chunks_collection":"chunks_gemma","folders_collection":"folders_gemma","search_mode":"flat"}}}`

func TestEmbeddingProfilesStrictJSON(t *testing.T) {
	if _, err := ParseEmbeddingProfiles([]byte(testProfiles)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		strings.Replace(testProfiles, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(testProfiles, `"dimensions":768`, `"dimensions":768,"dimensions":768`, 1),
		strings.Replace(testProfiles, `"provider":"llamacpp"`, `"typo":true,"provider":"llamacpp"`, 1),
		testProfiles + `{}`, `null`,
	} {
		if _, err := ParseEmbeddingProfiles([]byte(data)); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
}
func TestEmbeddingBindingsRequireExplicitThresholds(t *testing.T) {
	p, err := ParseEmbeddingProfiles([]byte(testProfiles))
	if err != nil {
		t.Fatal(err)
	}
	c := Config{EmbeddingProfiles: p, EnableRAG: true}
	if err := c.validateEmbeddingBindings(false); err != nil {
		t.Fatal(err)
	}
	p.Bindings.Memory.Thresholds = nil
	if err := c.validateEmbeddingBindings(false); err == nil {
		t.Fatal("inherited legacy thresholds")
	}
	// Standalone indexer has no fact consumer and no need to read its cloud key.
	if err := c.validateEmbeddingBindings(true); err != nil {
		t.Fatal(err)
	}
	p.Bindings.RAG.FoldersCollection = p.Bindings.RAG.ChunksCollection
	if err := c.validateEmbeddingBindings(true); err == nil {
		t.Fatal("accepted collection collision")
	}
}
func TestEmbeddingMemoryAndRAGCannotCollide(t *testing.T) {
	p, err := ParseEmbeddingProfiles([]byte(testProfiles))
	if err != nil {
		t.Fatal(err)
	}
	p.Bindings.Memory.Collection = p.Bindings.RAG.ChunksCollection
	c := Config{EmbeddingProfiles: p, EnableRAG: true}
	if err := c.validateEmbeddingBindings(false); err == nil {
		t.Fatal("accepted shared collection")
	}
}

func TestShippedEmbeddingProfilesKeepLegacyBindingsAndInactiveCloud(t *testing.T) {
	p, err := LoadEmbeddingProfiles("../../docs/examples/embedding-backends/profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	c := Config{EmbeddingProfiles: p, EnableRAG: true}
	if err := c.validateEmbeddingBindings(false); err != nil {
		t.Fatal(err)
	}
	if p.Bindings.Memory.Profile != "e5" || p.Bindings.RAG.Profile != "e5" {
		t.Fatal("example activates unapproved data egress or migration")
	}
	if p.Profiles["gemma"].Provider != "llamacpp" || p.Profiles["openai"].Provider != "openai" || p.Profiles["google"].Provider != "google" {
		t.Fatal("missing provider example")
	}
}

func TestMaintenanceCollectionSelectionDoesNotReadCloudKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(testProfiles), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBED_PROFILES_FILE", path)
	name, err := MemoryCollectionFromEnvironment()
	if err != nil || name != "memory_gemma" {
		t.Fatalf("collection=%s err=%v", name, err)
	}
}
