//go:build !darwin && !linux

package qdrant

import (
	"fmt"
	"os"
)

type snapshotArchiveDestination struct{}

func openSnapshotArchiveDestination(string) (*snapshotArchiveDestination, error) {
	return nil, fmt.Errorf("secure snapshot archival is unsupported on this platform")
}
func (*snapshotArchiveDestination) close() error                          { return nil }
func (*snapshotArchiveDestination) digest() (string, bool, error)         { return "", false, nil }
func (*snapshotArchiveDestination) createTemp() (*os.File, string, error) { return nil, "", nil }
func (*snapshotArchiveDestination) removeTemp(string)                     {}
func (*snapshotArchiveDestination) publish(string) error                  { return nil }
