package aimaintenance

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"golang.org/x/sys/unix"
)

const reviewSchema = 1

type ReviewAssessmentRequest struct {
	ExpectedRevision  int64  `json:"expected_revision"`
	Verdict           string `json:"verdict"`
	Reason            string `json:"reason"`
	Note              string `json:"note"`
	BaseEntryHash     string `json:"base_entry_hash"`
	ProposedEntryHash string `json:"proposed_entry_hash"`
	CurrentEntryHash  string `json:"current_entry_hash"`
}
type ReviewAssessment struct {
	SchemaVersion int                     `json:"schema_version"`
	Revision      int64                   `json:"revision"`
	RunID         string                  `json:"run_id"`
	SHA256        string                  `json:"sha256"`
	ProjectID     string                  `json:"project_id"`
	Request       ReviewAssessmentRequest `json:"request"`
	SavedAt       string                  `json:"saved_at"`
}
type ReviewAssessmentView struct {
	ReviewAssessment
	Drift bool `json:"drift"`
}
type reviewHead struct {
	SchemaVersion int   `json:"schema_version"`
	Revision      int64 `json:"revision"`
}

// SaveReviewAssessment appends one private assessment revision. It never edits
// run history, proposal artifacts, facts, or catalog state.
func SaveReviewAssessment(cfg aipolicy.Config, runID, digest, projectID string, req ReviewAssessmentRequest) (ReviewAssessment, bool, error) {
	if !runIDRE.MatchString(runID) || !isSHA(digest) || !isSHA(projectID) || !validReviewRequest(req) {
		return ReviewAssessment{}, false, errors.New("invalid assessment identity or request")
	}
	root := filepath.Join(cfg.StateDir, "maintenance-reviews")
	if err := aipolicy.SecureDir(root); err != nil {
		return ReviewAssessment{}, false, err
	}
	dir := filepath.Join(root, runID, digest, projectID)
	if err := aipolicy.SecureDir(dir); err != nil {
		return ReviewAssessment{}, false, err
	}
	fd, err := unix.Open(filepath.Join(dir, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ReviewAssessment{}, false, err
	}
	lock := os.NewFile(uintptr(fd), "review-lock")
	defer lock.Close()
	if err := lock.Chmod(0600); err != nil {
		return ReviewAssessment{}, false, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		return ReviewAssessment{}, false, err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	headPath := filepath.Join(dir, "head.json")
	head := reviewHead{}
	_, statErr := os.Lstat(headPath)
	if statErr == nil {
		raw, err := aipolicy.ReadPrivate(headPath, 1024)
		if err != nil {
			return ReviewAssessment{}, false, err
		}
		if strictReviewJSON(raw, &head) != nil || head.SchemaVersion != reviewSchema || head.Revision < 1 {
			return ReviewAssessment{}, false, errors.New("corrupt assessment head")
		}
	} else if !os.IsNotExist(statErr) {
		return ReviewAssessment{}, false, statErr
	}
	if head.Revision > 0 {
		prior, er := readReviewRevision(dir, head.Revision, runID, digest, projectID)
		if er != nil {
			return ReviewAssessment{}, false, er
		}
		if prior.Request == req {
			return prior, false, nil
		}
	}
	if head.Revision != req.ExpectedRevision {
		orphanRevision := head.Revision + 1
		if _, orphanErr := readReviewRevision(dir, orphanRevision, runID, digest, projectID); orphanErr == nil {
			if err := writeReviewHead(dir, reviewHead{SchemaVersion: reviewSchema, Revision: orphanRevision}); err != nil {
				return ReviewAssessment{}, false, err
			}
		}
		return ReviewAssessment{}, true, nil
	}
	n := head.Revision + 1
	record := ReviewAssessment{SchemaVersion: reviewSchema, Revision: n, RunID: runID, SHA256: digest, ProjectID: projectID, Request: req, SavedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	name := fmt.Sprintf("revision-%08d.json", n)
	if prior, er := readReviewRevision(dir, n, runID, digest, projectID); er == nil {
		if prior.Request != req || prior.Revision != n {
			if err := writeReviewHead(dir, reviewHead{SchemaVersion: reviewSchema, Revision: n}); err != nil {
				return ReviewAssessment{}, false, err
			}
			return ReviewAssessment{}, true, nil
		}
		record = prior // Recover a landed immutable revision whose head was not published.
	} else if !os.IsNotExist(er) {
		return ReviewAssessment{}, false, er
	} else {
		if _, statErr := os.Lstat(filepath.Join(dir, name)); statErr == nil {
			return ReviewAssessment{}, false, errors.New("invalid orphan revision")
		} else if !os.IsNotExist(statErr) {
			return ReviewAssessment{}, false, statErr
		}
		b, _ := json.Marshal(record)
		if err := writeReviewImmutable(dir, name, b); err != nil {
			return ReviewAssessment{}, false, err
		}
	}
	if err := writeReviewHead(dir, reviewHead{SchemaVersion: reviewSchema, Revision: n}); err != nil {
		return ReviewAssessment{}, false, err
	}
	return record, false, nil
}

func ReadReviewAssessments(cfg aipolicy.Config, runID, digest string) ([]ReviewAssessment, error) {
	if !runIDRE.MatchString(runID) || !isSHA(digest) {
		return nil, errors.New("invalid assessment identity")
	}
	base := filepath.Join(cfg.StateDir, "maintenance-reviews")
	for _, p := range []string{base, filepath.Join(base, runID), filepath.Join(base, runID, digest)} {
		i, e := os.Lstat(p)
		if os.IsNotExist(e) {
			return []ReviewAssessment{}, nil
		}
		if e != nil {
			return nil, e
		}
		if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("unsafe assessment directory")
		}
	}
	entries, err := os.ReadDir(filepath.Join(base, runID, digest))
	if err != nil {
		return nil, err
	}
	out := []ReviewAssessment{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("unsafe assessment directory")
		}
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if !isSHA(id) {
			continue
		}
		dir := filepath.Join(base, runID, digest, id)
		if !safeReviewDir(dir) {
			return nil, errors.New("unsafe assessment directory")
		}
		headPath := filepath.Join(dir, "head.json")
		if _, e := os.Lstat(headPath); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return nil, e
		}
		b, e := aipolicy.ReadPrivate(headPath, 1024)
		if e != nil {
			return nil, e
		}
		var head reviewHead
		if strictReviewJSON(b, &head) != nil || head.SchemaVersion != reviewSchema || head.Revision < 1 {
			return nil, errors.New("corrupt assessment head")
		}
		rec, e := readReviewRevision(dir, head.Revision, runID, digest, id)
		if e != nil {
			return nil, e
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out, nil
}

func readReviewRevision(dir string, n int64, run, digest, id string) (ReviewAssessment, error) {
	var rec ReviewAssessment
	path := filepath.Join(dir, fmt.Sprintf("revision-%08d.json", n))
	if _, err := os.Lstat(path); err != nil {
		return rec, err
	}
	b, err := aipolicy.ReadPrivate(path, 16<<10)
	if err != nil {
		return rec, err
	}
	if strictReviewJSON(b, &rec) != nil || rec.SchemaVersion != reviewSchema || rec.Revision != n || rec.RunID != run || rec.SHA256 != digest || rec.ProjectID != id || !validReviewRequest(rec.Request) {
		return rec, errors.New("corrupt assessment revision")
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.SavedAt); err != nil {
		return rec, errors.New("corrupt assessment timestamp")
	}
	return rec, nil
}
func writeReviewImmutable(dir, name string, b []byte) error {
	fd, err := unix.Open(filepath.Join(dir, name), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func writeReviewHead(dir string, h reviewHead) error {
	b, _ := json.Marshal(h)
	return aipolicy.AtomicWrite(dir, "head.json", b)
}
func strictReviewJSON(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("extra json value")
	}
	return nil
}
func isSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func safeReviewDir(path string) bool {
	i, e := os.Lstat(path)
	return e == nil && i.IsDir() && i.Mode()&os.ModeSymlink == 0
}
func validReviewRequest(r ReviewAssessmentRequest) bool {
	return r.ExpectedRevision >= 0 && map[string]bool{"acceptable": true, "needs_correction": true, "rejected": true}[r.Verdict] && map[string]bool{"none": true, "source_mismatch": true, "proposal_error": true, "insufficient_evidence": true, "identity_mismatch": true, "other": true}[r.Reason] && len(r.Note) <= 2048 && utf8.ValidString(r.Note) && isSHA(r.BaseEntryHash) && isSHA(r.ProposedEntryHash) && (r.CurrentEntryHash == "" || isSHA(r.CurrentEntryHash))
}
