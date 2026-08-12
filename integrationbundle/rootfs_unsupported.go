//go:build !darwin && !linux

package integrationbundle

import (
	"context"
	"fmt"
	"io/fs"
)

func platformFileIdentity(fs.FileInfo) (uint64, uint64) { return 0, 0 }

type UnsupportedMutationPlatformError struct{ Operation string }
type mutationRoot struct{}
type rootLock struct{}

func (*rootLock) Close() error { return nil }
func (*mutationRoot) identity() (uint64, uint64, error) {
	return 0, 0, &UnsupportedMutationPlatformError{Operation: "root_identity"}
}
func (*mutationRoot) lock(context.Context, bool) (*rootLock, error) {
	return nil, &UnsupportedMutationPlatformError{Operation: "lock"}
}

func openMutationRoot(string) (*mutationRoot, error) {
	return &mutationRoot{}, nil
}
func (*mutationRoot) Close() error { return nil }

func (e *UnsupportedMutationPlatformError) Error() string {
	return fmt.Sprintf("safe integration filesystem mutation %q is unsupported on this platform", e.Operation)
}
func atomicWriteRelative(*mutationRoot, string, []byte, fs.FileMode, string) error {
	return &UnsupportedMutationPlatformError{Operation: "atomic_write"}
}
func removeRelative(*mutationRoot, string) error {
	return &UnsupportedMutationPlatformError{Operation: "remove"}
}
func removeAnyRelative(*mutationRoot, string) error {
	return &UnsupportedMutationPlatformError{Operation: "remove_tree"}
}
func ensureDirRelative(*mutationRoot, string) error {
	return &UnsupportedMutationPlatformError{Operation: "mkdir"}
}
