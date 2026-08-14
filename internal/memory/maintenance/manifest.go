package maintenance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

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
