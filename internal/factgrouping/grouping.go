// Package factgrouping applies explicitly reviewed grouping manifests with stopped writers.
package factgrouping

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type Group struct {
	Tags       []string `json:"tags"`
	PrimaryTag string   `json:"primary_tag"`
	AuditRef   string   `json:"audit_ref"`
}
type Change struct {
	PointID     string `json:"point_id"`
	Fingerprint string `json:"fingerprint"`
	Before      Group  `json:"before"`
	After       Group  `json:"after"`
}
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	CatalogHash   string   `json:"catalog_hash"`
	Changes       []Change `json:"changes"`
	Approved      bool     `json:"approved"`
}
type Outcome struct {
	PointID string `json:"point_id"`
	Status  string `json:"status"`
}
type Journal struct {
	SchemaVersion int       `json:"schema_version"`
	ManifestHash  string    `json:"manifest_hash"`
	Operation     string    `json:"operation"`
	Outcomes      []Outcome `json:"outcomes"`
	UpdatedAt     string    `json:"updated_at"`
}
type Store interface {
	Get(context.Context, string) (qdrant.Point, bool, error)
	SetGrouping(context.Context, string, []string, string, string) error
}

var validID = regexp.MustCompile(`^(?:[0-9]+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)
var validHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Fingerprint excludes grouping itself and best-effort read counters; grouping is compared separately.
func Fingerprint(payload map[string]interface{}) (string, error) {
	copy := map[string]interface{}{}
	for k, v := range payload {
		switch k {
		case "tags", "primary_tag", "ai_grouping_ref", "recall_count", "last_recalled_at":
			continue
		}
		copy[k] = v
	}
	b, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func GroupFrom(payload map[string]interface{}) Group {
	g := Group{Tags: []string{}}
	switch v := payload["tags"].(type) {
	case []string:
		g.Tags = append(g.Tags, v...)
	case []interface{}:
		for _, t := range v {
			if s, ok := t.(string); ok {
				g.Tags = append(g.Tags, s)
			}
		}
	}
	g.PrimaryTag, _ = payload["primary_tag"].(string)
	g.AuditRef, _ = payload["ai_grouping_ref"].(string)
	return g
}
func (m Manifest) Validate() error {
	if m.SchemaVersion != 1 || !m.Approved || m.ID == "" || len(m.ID) > 128 || !validHash.MatchString(m.CatalogHash) || len(m.Changes) == 0 || len(m.Changes) > 100 {
		return errors.New("invalid or unapproved grouping manifest")
	}
	seen := map[string]bool{}
	for _, c := range m.Changes {
		if !validID.MatchString(c.PointID) || seen[c.PointID] || !validHash.MatchString(c.Fingerprint) {
			return errors.New("invalid grouping change identity")
		}
		seen[c.PointID] = true
		for _, g := range []Group{c.Before, c.After} {
			if len(g.Tags) > 100 || len(g.AuditRef) > 128 {
				return errors.New("grouping exceeds limits")
			}
			has := g.PrimaryTag == ""
			tags := map[string]bool{}
			for _, t := range g.Tags {
				if strings.TrimSpace(t) == "" || len(t) > 255 || tags[t] {
					return errors.New("invalid grouping tag")
				}
				tags[t] = true
				if t == g.PrimaryTag {
					has = true
				}
			}
			if !has {
				return errors.New("primary grouping must occur in tags")
			}
		}
	}
	return nil
}
func ReadManifest(path string) (Manifest, error) {
	b, err := aipolicy.ReadPrivate(path, 1<<20)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return m, errors.New("invalid grouping manifest JSON")
	}
	return m, m.Validate()
}
func Hash(m Manifest) string {
	b, _ := json.Marshal(m)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func save(path string, j Journal) error {
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return aipolicy.AtomicWrite(filepath.Dir(path), filepath.Base(path), b)
}

// Run requires an explicit stopped-writer window for apply and rollback.
// A transport error after dispatch is ambiguous, never automatic success or retry.
func Run(ctx context.Context, store Store, m Manifest, journalPath string, stopped, rollback bool) (Journal, error) {
	if err := m.Validate(); err != nil {
		return Journal{}, err
	}
	if !stopped {
		return Journal{}, errors.New("grouping requires confirmed stopped writers")
	}
	if journalPath == "" {
		return Journal{}, errors.New("private journal path required")
	}
	if err := aipolicy.SecureDir(filepath.Dir(journalPath)); err != nil {
		return Journal{}, err
	}
	fd, err := unix.Open(filepath.Join(filepath.Dir(journalPath), ".grouping.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return Journal{}, errors.New("cannot open grouping lock")
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return Journal{}, errors.New("grouping operation already running")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	operation := "apply"
	if rollback {
		operation = "rollback"
	}
	j := Journal{SchemaVersion: 1, ManifestHash: Hash(m), Operation: operation}
	if b, err := aipolicy.ReadPrivate(journalPath, 1<<20); err == nil {
		if json.Unmarshal(b, &j) != nil || j.SchemaVersion != 1 || j.ManifestHash != Hash(m) || j.Operation != operation {
			return j, errors.New("journal identity mismatch")
		}
	} else if _, statErr := os.Lstat(journalPath); !os.IsNotExist(statErr) {
		return j, errors.New("cannot read grouping journal")
	}
	byID := map[string]string{}
	manifestIDs := map[string]bool{}
	for _, c := range m.Changes {
		manifestIDs[c.PointID] = true
	}
	for _, o := range j.Outcomes {
		validStatus := o.Status == "dispatching" || o.Status == "ambiguous" || o.Status == "updated" || o.Status == "already_applied" || o.Status == "conflict" || o.Status == "not_found"
		if byID[o.PointID] != "" || !manifestIDs[o.PointID] || !validStatus {
			return j, errors.New("duplicate journal outcome")
		}
		byID[o.PointID] = o.Status
	}
	for _, c := range m.Changes {
		if err := ctx.Err(); err != nil {
			return j, err
		}
		point, found, err := store.Get(ctx, c.PointID)
		if err != nil {
			return j, errors.New("cannot recheck grouping point")
		}
		expected, target := c.Before, c.After
		if rollback {
			expected, target = target, expected
		}
		finger, _ := Fingerprint(point.Payload)
		actual := GroupFrom(point.Payload)
		status := "conflict"
		if !found {
			status = "not_found"
		} else if finger == c.Fingerprint && reflect.DeepEqual(actual, target) {
			status = "already_applied"
		} else if finger == c.Fingerprint && reflect.DeepEqual(actual, expected) {
			if byID[c.PointID] == "dispatching" || byID[c.PointID] == "ambiguous" {
				status = "ambiguous"
			} else {
				status = "ready"
			}
		}
		set := func(status string) {
			for i := range j.Outcomes {
				if j.Outcomes[i].PointID == c.PointID {
					j.Outcomes[i].Status = status
					return
				}
			}
			j.Outcomes = append(j.Outcomes, Outcome{c.PointID, status})
		}
		if status != "ready" {
			set(status)
			if err := save(journalPath, j); err != nil {
				return j, err
			}
			continue
		}
		set("dispatching")
		if err := save(journalPath, j); err != nil {
			return j, err
		}
		err = store.SetGrouping(ctx, c.PointID, target.Tags, target.PrimaryTag, target.AuditRef)
		if err != nil {
			set("ambiguous")
			if e := save(journalPath, j); e != nil {
				return j, e
			}
			return j, errors.New("grouping dispatch outcome ambiguous; inspect before retry")
		}
		verify, ok, err := store.Get(ctx, c.PointID)
		if err != nil || !ok {
			set("ambiguous")
		} else if vf, _ := Fingerprint(verify.Payload); vf != c.Fingerprint || !reflect.DeepEqual(GroupFrom(verify.Payload), target) {
			set("ambiguous")
		} else {
			set("updated")
		}
		if err := save(journalPath, j); err != nil {
			return j, err
		}
	}
	for _, o := range j.Outcomes {
		if o.Status != "updated" && o.Status != "already_applied" {
			return j, fmt.Errorf("grouping operation incomplete: %s", o.Status)
		}
	}
	return j, nil
}
