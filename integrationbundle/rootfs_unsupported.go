//go:build !darwin && !linux

package integrationbundle

import (
	"context"
	"fmt"
	"io/fs"
)

func platformFileIdentity(fs.FileInfo) (uint64, uint64) { return 0, 0 }

type UnsupportedMutationPlatformError struct{ Operation string }
type mutationRoot struct {
	dev uint64
	ino uint64
}
type rootLock struct{}

func (*rootLock) Close() error { return nil }
func (m *mutationRoot) identity() (uint64, uint64, error) {
	return m.dev, m.ino, nil
}
func (*mutationRoot) lock(_ context.Context, exclusive bool) (*rootLock, error) {
	if exclusive {
		return nil, &UnsupportedMutationPlatformError{Operation: "lock"}
	}
	return &rootLock{}, nil
}

func openMutationRoot(_ string, dev, ino uint64) (*mutationRoot, error) {
	return &mutationRoot{dev: dev, ino: ino}, nil
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
