//go:build darwin || linux

package integrationbundle

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
	"golang.org/x/sys/unix"
)

func TestInstallRecoversWritePublishedBeforeDirectorySyncFailure(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	failed := false
	_, err := Install(InstallOptions{TargetRoot: root, set: set, beforeApply: func(r *rootContext) error {
		realSync := r.mutation.syncDir
		r.mutation.syncDir = func(fd int) error {
			var stat unix.Stat_t
			if !failed && unix.Fstatat(fd, "AGENTS.md", &stat, unix.AT_SYMLINK_NOFOLLOW) == nil {
				failed = true
				return unix.EIO
			}
			return realSync(fd)
		}
		return nil
	}})
	if err == nil || !failed {
		t.Fatalf("expected injected directory sync failure, err=%v failed=%t", err, failed)
	}
	if _, readErr := safeRead(root, "AGENTS.md"); !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("published artifact remained after recovery: %v", readErr)
	}
	if _, readErr := safeRead(root, statePath(set.ClientID)); !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("installation state remained after recovery: %v", readErr)
	}
}
