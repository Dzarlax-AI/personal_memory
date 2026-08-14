package maintenance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxManifestBytes bounds an operator-supplied analysis report before JSON
// decoding. Manifests never need to contain source fact text or vectors.
const MaxManifestBytes int64 = 16 << 20

func WriteManifest(path string, manifest Manifest) error {
	if path == "" {
		return fmt.Errorf("output path is required")
	}
	if !manifest.Complete || manifest.BatchID == "" {
		return fmt.Errorf("only complete actionable manifests may be written")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode analysis manifest: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create analysis manifest: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write analysis manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync analysis manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close analysis manifest: %w", err)
	}
	return nil
}

// ReadManifest accepts one bounded, strict JSON manifest. It deliberately
// rejects unknown fields and trailing values so a mutation cannot silently use
// a partially understood report.
func ReadManifest(path string) (Manifest, error) {
	if path == "" {
		return Manifest{}, fmt.Errorf("manifest path is required")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return Manifest{}, fmt.Errorf("open manifest")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil || int64(len(data)) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("read manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest")
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("decode manifest")
	}
	return manifest, nil
}
