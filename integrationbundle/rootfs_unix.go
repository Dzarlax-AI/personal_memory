//go:build darwin || linux

package integrationbundle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func platformFileIdentity(info fs.FileInfo) (uint64, uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}

type mutationRoot struct{ fd int }

func openMutationRoot(rootName string) (*mutationRoot, error) {
	fd, err := unix.Open(rootName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return &mutationRoot{fd: fd}, nil
}
func (m *mutationRoot) Close() error { return unix.Close(m.fd) }
func (m *mutationRoot) identity() (uint64, uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(m.fd, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

type rootLock struct {
	fd      int
	closeFD bool
}

func (l *rootLock) Close() error {
	err := unix.Flock(l.fd, unix.LOCK_UN)
	if l.closeFD {
		err = errors.Join(err, unix.Close(l.fd))
	}
	return err
}
func (m *mutationRoot) lock(ctx context.Context, exclusive bool) (*rootLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lfd, e := unix.Dup(m.fd)
	if e != nil {
		return nil, e
	}
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	for {
		e = unix.Flock(lfd, operation|unix.LOCK_NB)
		if e == nil {
			return &rootLock{fd: lfd, closeFD: true}, nil
		}
		if !errors.Is(e, unix.EWOULDBLOCK) && !errors.Is(e, unix.EAGAIN) {
			unix.Close(lfd)
			return nil, e
		}
		select {
		case <-ctx.Done():
			unix.Close(lfd)
			return nil, fmt.Errorf("integration lock: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func withParentFD(root *mutationRoot, relative string, create bool, fn func(int, string) error) error {
	if !installerSafePath(relative) {
		return fmt.Errorf("unsafe path")
	}
	fd, err := unix.Dup(root.fd)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(e, unix.ENOENT) && create {
			if e = unix.Mkdirat(fd, part, 0o700); e != nil && !errors.Is(e, unix.EEXIST) {
				return e
			}
			if e = unix.Fsync(fd); e != nil {
				return e
			}
			next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if e != nil {
			return e
		}
		_ = unix.Close(fd)
		fd = next
	}
	return fn(fd, parts[len(parts)-1])
}
func atomicWriteRelative(root *mutationRoot, relative string, data []byte, mode fs.FileMode, tempName string) error {
	return withParentFD(root, relative, true, func(fd int, base string) error {
		tmpFD, err := unix.Openat(fd, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(tmpFD), tempName)
		cleanup := func() { file.Close(); _ = unix.Unlinkat(fd, tempName, 0) }
		if _, err = file.Write(data); err != nil {
			cleanup()
			return err
		}
		if err = file.Sync(); err != nil {
			cleanup()
			return err
		}
		if err = file.Close(); err != nil {
			_ = unix.Unlinkat(fd, tempName, 0)
			return err
		}
		var st unix.Stat_t
		if err = unix.Fstatat(fd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
			_ = unix.Unlinkat(fd, tempName, 0)
			return fmt.Errorf("final symlink rejected")
		}
		if errors.Is(err, unix.ENOENT) {
			if err = unix.Linkat(fd, tempName, fd, base, 0); err != nil {
				_ = unix.Unlinkat(fd, tempName, 0)
				return err
			}
			if err = unix.Unlinkat(fd, tempName, 0); err != nil {
				return err
			}
			return unix.Fsync(fd)
		}
		if err != nil {
			_ = unix.Unlinkat(fd, tempName, 0)
			return err
		}
		if err = unix.Renameat(fd, tempName, fd, base); err != nil {
			_ = unix.Unlinkat(fd, tempName, 0)
			return err
		}
		return unix.Fsync(fd)
	})
}
func removeRelative(root *mutationRoot, relative string) error {
	return withParentFD(root, relative, false, func(fd int, base string) error {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return fmt.Errorf("symlink removal rejected")
		}
		if err := unix.Unlinkat(fd, base, 0); err != nil {
			return err
		}
		return unix.Fsync(fd)
	})
}
func removeAnyRelative(root *mutationRoot, relative string) error {
	return withParentFD(root, relative, false, func(fd int, base string) error {
		if err := unix.Unlinkat(fd, base, 0); err != nil {
			if err = unix.Unlinkat(fd, base, unix.AT_REMOVEDIR); err != nil {
				return err
			}
		}
		return unix.Fsync(fd)
	})
}
func ensureDirRelative(root *mutationRoot, dir string) error {
	if dir == "." {
		return nil
	}
	return withParentFD(root, path.Join(dir, ".probe"), true, func(_ int, _ string) error { return nil })
}
