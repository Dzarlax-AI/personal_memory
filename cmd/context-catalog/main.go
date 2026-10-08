// Command context-catalog validates, stages, exports, and publishes reviewed
// context catalog snapshots. It does not call AI providers or modify facts.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"golang.org/x/sys/unix"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: context-catalog <validate|import|export|publish> [flags]")
	}
	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("validate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		input := fs.String("file", "", "snapshot JSON file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || fs.NArg() != 0 {
			return errors.New("validate requires -file and accepts no positional arguments")
		}
		s, err := readSnapshot(*input)
		if err != nil {
			return err
		}
		hash, err := contextcatalog.Hash(s)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "valid entries=%d hash=%s\n", len(s.Entries), hash)
		return err
	case "import":
		fs := flag.NewFlagSet("import", flag.ContinueOnError)
		fs.SetOutput(stderr)
		input := fs.String("file", "", "source snapshot JSON file")
		output := fs.String("output", "", "new staging JSON file (must not already exist)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || *output == "" || fs.NArg() != 0 {
			return errors.New("import requires -file and -output and accepts no positional arguments")
		}
		s, err := readSnapshot(*input)
		if err != nil {
			return err
		}
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if err := writeNew(*output, b); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "imported validated snapshot to %s\n", filepath.Clean(*output))
		return err
	case "export":
		fs := flag.NewFlagSet("export", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dir := fs.String("dir", "", "catalog state directory")
		output := fs.String("output", "", "output file (default stdout)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || fs.NArg() != 0 {
			return errors.New("export requires -dir and accepts no positional arguments")
		}
		s, hash, err := contextcatalog.LoadActive(*dir)
		if err != nil {
			return err
		}
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if *output == "" {
			_, err = stdout.Write(b)
			return err
		}
		if err := writeNew(*output, b); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stderr, "exported hash=%s\n", hash)
		return err
	case "publish":
		fs := flag.NewFlagSet("publish", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dir := fs.String("dir", "", "catalog state directory")
		input := fs.String("file", "", "reviewed snapshot JSON file")
		expected := fs.String("expected-hash", "", "active digest reviewed by the operator; empty only for initial publish")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || *input == "" || fs.NArg() != 0 {
			return errors.New("publish requires -dir and -file and accepts no positional arguments")
		}
		s, err := readSnapshot(*input)
		if err != nil {
			return err
		}
		hash, err := contextcatalog.Publish(*dir, s, *expected)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "published hash=%s entries=%d\n", hash, len(s.Entries))
		return err
	default:
		return fmt.Errorf("unknown command %q; use validate, import, export, or publish", args[0])
	}
}

func readSnapshot(path string) (contextcatalog.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return contextcatalog.Snapshot{}, fmt.Errorf("open input snapshot: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, contextcatalog.MaxSnapshotBytes+1))
	if err != nil {
		return contextcatalog.Snapshot{}, fmt.Errorf("read input snapshot: %w", err)
	}
	if len(raw) > contextcatalog.MaxSnapshotBytes {
		return contextcatalog.Snapshot{}, fmt.Errorf("input snapshot exceeds maximum of %d bytes", contextcatalog.MaxSnapshotBytes)
	}
	var s contextcatalog.Snapshot
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return contextcatalog.Snapshot{}, fmt.Errorf("decode input snapshot: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return contextcatalog.Snapshot{}, errors.New("input must contain exactly one JSON value")
	}
	if err := contextcatalog.Validate(s); err != nil {
		return contextcatalog.Snapshot{}, err
	}
	return s, nil
}

func writeNew(path string, data []byte) error {
	if info, err := os.Lstat(filepath.Dir(path)); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("output parent must be an existing non-symlink directory")
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("create output file (must not exist): %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write output: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("sync output: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}
