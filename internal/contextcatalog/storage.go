package contextcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const (
	activeName      = "active.json"
	snapshotDirName = "snapshots"
	lockName        = ".publish.lock"
)

var tempSequence atomic.Uint64

type activePointer struct {
	SnapshotHash string `json:"snapshot_hash"`
}

// LoadActive returns a validated private copy of the snapshot and its digest.
func LoadActive(dir string) (Snapshot, string, error) {
	if err := checkDirTree(dir, false); err != nil {
		return Snapshot{}, "", err
	}
	activePath := filepath.Join(dir, activeName)
	f, err := openRegular(activePath, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, "", ErrNotFound
	}
	if err != nil {
		return Snapshot{}, "", err
	}
	defer f.Close()
	var pointer activePointer
	d := json.NewDecoder(io.LimitReader(f, 256))
	d.DisallowUnknownFields()
	if err := d.Decode(&pointer); err != nil {
		return Snapshot{}, "", fmt.Errorf("read active catalog pointer: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Snapshot{}, "", errors.New("active pointer must contain exactly one JSON value")
	}
	if !sha256Pattern.MatchString(pointer.SnapshotHash) {
		return Snapshot{}, "", errors.New("active pointer has an invalid snapshot hash")
	}
	if err := rejectSymlink(filepath.Join(dir, snapshotDirName)); err != nil {
		return Snapshot{}, "", err
	}
	path := filepath.Join(dir, snapshotDirName, pointer.SnapshotHash+".json")
	snapshotFile, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return Snapshot{}, "", fmt.Errorf("open active catalog snapshot: %w", err)
	}
	defer snapshotFile.Close()
	s, err := decodeSnapshot(snapshotFile)
	if err != nil {
		return Snapshot{}, "", err
	}
	hash, err := Hash(s)
	if err != nil {
		return Snapshot{}, "", err
	}
	if hash != pointer.SnapshotHash {
		return Snapshot{}, "", errors.New("active snapshot content does not match its digest")
	}
	return cloneSnapshot(s), hash, nil
}

// Publish writes an immutable snapshot and atomically advances active.json only
// when the currently active digest matches expectedActiveHash. An empty expected
// hash is valid only before the first publication.
func Publish(dir string, snapshot Snapshot, expectedActiveHash string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("catalog directory is required")
	}
	if err := Validate(snapshot); err != nil {
		return "", err
	}
	if snapshot.ParentHash != expectedActiveHash {
		return "", errors.New("snapshot parent_hash must match expected active hash")
	}
	for _, entry := range snapshot.Entries {
		if !Eligible(entry) {
			return "", errors.New("publication requires every catalog entry to be eligible")
		}
	}
	if err := ensureDirTree(dir); err != nil {
		return "", err
	}
	unlock, err := acquireLock(filepath.Join(dir, lockName))
	if err != nil {
		return "", err
	}
	defer unlock()
	return publishLocked(dir, snapshot, expectedActiveHash)
}

// publishLocked publishes while the caller holds lockName. Keeping this
// helper separate lets registry mutations compare, edit, and publish one
// generation without releasing the same lock or deadlocking in Publish.
func publishLocked(dir string, snapshot Snapshot, expectedActiveHash string) (string, error) {
	if err := ensureDirTree(dir); err != nil {
		return "", err
	}
	if err := Validate(snapshot); err != nil {
		return "", err
	}
	if snapshot.ParentHash != expectedActiveHash {
		return "", errors.New("snapshot parent_hash must match expected active hash")
	}
	for _, entry := range snapshot.Entries {
		if !Eligible(entry) {
			return "", errors.New("publication requires every catalog entry to be eligible")
		}
	}
	hash, err := Hash(snapshot)
	if err != nil {
		return "", err
	}
	currentHash, err := activeHash(dir)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if currentHash != expectedActiveHash {
		return "", ErrConflict
	}

	encoded, err := canonicalJSON(cloneSnapshot(snapshot))
	if err != nil {
		return "", err
	}
	snapshotsDir := filepath.Join(dir, snapshotDirName)
	if err := mkdirPrivate(snapshotsDir); err != nil {
		return "", err
	}
	if err := rejectSymlink(snapshotsDir); err != nil {
		return "", err
	}
	snapshotPath := filepath.Join(snapshotsDir, hash+".json")
	if existing, err := openRegular(snapshotPath, os.O_RDONLY, 0); err == nil {
		got, readErr := io.ReadAll(io.LimitReader(existing, MaxSnapshotBytes+1))
		closeErr := existing.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if !bytes.Equal(got, encoded) {
			return "", errors.New("immutable snapshot path already contains different data")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		if err := writeExclusive(snapshotPath, encoded); err != nil {
			return "", err
		}
		if err := syncDir(snapshotsDir); err != nil {
			return "", err
		}
	}

	pointerBytes, _ := json.Marshal(activePointer{SnapshotHash: hash})
	pointerBytes = append(pointerBytes, '\n')
	if err := atomicReplace(dir, activeName, pointerBytes); err != nil {
		return "", err
	}
	return hash, nil
}

func activeHash(dir string) (string, error) {
	_, hash, err := LoadActive(dir)
	return hash, err
}

func ensureDirTree(dir string) error {
	return aipolicy.SecureDir(dir)
}

func checkDirTree(dir string, create bool) error {
	if create {
		return ensureDirTree(dir)
	}
	full, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(full, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe catalog directory ancestor")
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect catalog directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("catalog directory cannot be a symlink")
	}
	if !info.IsDir() {
		return errors.New("catalog path is not a directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("catalog directory must be private")
	}
	return nil
}

func mkdirPrivate(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create catalog snapshot directory: %w", err)
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("catalog snapshots path is not a directory")
	}
	return os.Chmod(path, 0700)
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect catalog path %s: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("catalog path %s cannot be a symlink", filepath.Base(path))
	}
	return nil
}

func openRegular(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("catalog file is not a regular file")
	}
	return f, nil
}

func acquireLock(path string) (func(), error) {
	f, err := openRegular(path, unix.O_CREAT|unix.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open catalog publication lock: %w", err)
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock catalog publication: %w", err)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}

func writeExclusive(path string, data []byte) error {
	f, err := openRegular(path, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create immutable catalog snapshot: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write catalog snapshot: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("sync catalog snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close catalog snapshot: %w", err)
	}
	return nil
}

func atomicReplace(dir, name string, data []byte) error {
	temp := filepath.Join(dir, fmt.Sprintf(".%s.%d.tmp", name, tempSequence.Add(1)))
	f, err := openRegular(temp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create active pointer temp file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(temp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write active pointer: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync active pointer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close active pointer: %w", err)
	}
	if err := os.Rename(temp, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("publish active catalog pointer: %w", err)
	}
	cleanup = false
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync catalog directory after publication: %w", err)
	}
	return nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
