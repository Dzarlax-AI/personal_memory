package qdrant

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const managedQdrantSnapshotDir = "/qdrant/snapshots"

func resolveSnapshotArchivePath(destination, managedRoot string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", fmt.Errorf("snapshot archive path is required")
	}
	absDestination, err := filepath.Abs(filepath.Clean(destination))
	if err != nil {
		return "", fmt.Errorf("resolve snapshot archive path: %w", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(absDestination))
	if err != nil {
		return "", fmt.Errorf("resolve snapshot archive parent: %w", err)
	}
	resolvedDestination := filepath.Join(resolvedParent, filepath.Base(absDestination))
	absManagedRoot, err := filepath.Abs(filepath.Clean(managedRoot))
	if err != nil {
		return "", fmt.Errorf("resolve managed snapshot directory: %w", err)
	}
	resolvedManagedRoot, err := filepath.EvalSymlinks(absManagedRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve managed snapshot directory: %w", err)
		}
		resolvedManagedRoot = absManagedRoot
	}
	for _, candidate := range []string{absDestination, resolvedDestination} {
		for _, root := range []string{absManagedRoot, resolvedManagedRoot} {
			relative, relErr := filepath.Rel(root, candidate)
			if relErr != nil {
				return "", fmt.Errorf("compare snapshot archive path: %w", relErr)
			}
			if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
				return "", fmt.Errorf("snapshot archive must be outside Qdrant's managed snapshot directory")
			}
		}
	}
	return resolvedDestination, nil
}
