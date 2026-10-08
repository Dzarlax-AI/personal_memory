package aipolicy

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSecureDirConcurrentBootstrap(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 20; attempt++ {
		dir := filepath.Join(root, string(rune('a'+attempt)), "nested", "state")
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- SecureDir(dir) }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("unsafe result: %v %v", info, err)
		}
	}
}

func TestSecureDirRejectsExistingSymlinkComponent(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := SecureDir(filepath.Join(link, "state")); err == nil {
		t.Fatal("symlink component accepted")
	}
	if _, err := os.Lstat(filepath.Join(target, "state")); !os.IsNotExist(err) {
		t.Fatalf("symlink target modified: %v", err)
	}
}
