//go:build darwin || linux

package qdrant

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type snapshotArchiveDestination struct {
	dirFD int
	base  string
}

func openSnapshotArchiveDestination(destination string) (*snapshotArchiveDestination, error) {
	resolved, err := resolveSnapshotArchivePath(destination, managedQdrantSnapshotDir)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(resolved), string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("open snapshot archive parent: %w", openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	return &snapshotArchiveDestination{dirFD: fd, base: filepath.Base(resolved)}, nil
}

func (d *snapshotArchiveDestination) close() error { return unix.Close(d.dirFD) }

func (d *snapshotArchiveDestination) digest() (string, bool, error) {
	fd, err := unix.Openat(d.dirFD, d.base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open snapshot archive: %w", err)
	}
	file := os.NewFile(uintptr(fd), d.base)
	digest, digestErr := snapshotArchiveFileDigest(file)
	closeErr := file.Close()
	if digestErr != nil {
		return "", false, digestErr
	}
	if closeErr != nil {
		return "", false, fmt.Errorf("close snapshot archive: %w", closeErr)
	}
	return digest, true, nil
}

func (d *snapshotArchiveDestination) createTemp() (*os.File, string, error) {
	for range 100 {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return nil, "", fmt.Errorf("generate snapshot archive temporary name: %w", err)
		}
		name := ".personal-memory-snapshot-" + hex.EncodeToString(random)
		fd, err := unix.Openat(d.dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create snapshot archive: %w", err)
		}
		return os.NewFile(uintptr(fd), name), name, nil
	}
	return nil, "", fmt.Errorf("create snapshot archive: temporary name collision")
}

func (d *snapshotArchiveDestination) removeTemp(name string) { _ = unix.Unlinkat(d.dirFD, name, 0) }

func (d *snapshotArchiveDestination) publish(name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(d.dirFD, d.base, &stat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Linkat(d.dirFD, name, d.dirFD, d.base, 0); err != nil {
		return err
	}
	if err := unix.Unlinkat(d.dirFD, name, 0); err != nil {
		return err
	}
	return unix.Fsync(d.dirFD)
}
