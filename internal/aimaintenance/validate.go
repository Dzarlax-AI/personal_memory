package aimaintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

var requestAliasRE = regexp.MustCompile(`^f[0-9]{1,3}$`)
var catalogEvidenceAliasRE = regexp.MustCompile(`^c[0-9]{1,3}$`)
var digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
var projectTagRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var missingEvidenceCodes = map[string]bool{
	"ambiguous_subject":        true,
	"missing_ownership":        true,
	"insufficient_evidence":    true,
	"catalog_gap":              true,
	"shared_ownership_unclear": true,
}

func validateInput(in Input) error {
	if err := contextcatalog.Validate(in.Catalog); err != nil {
		return fmt.Errorf("invalid maintenance catalog: %w", err)
	}
	if len(in.Facts) > 100 || (len(in.Facts) == 0 && len(in.CatalogEvidence) == 0) {
		return errors.New("maintenance input must contain facts or scoped catalog evidence")
	}
	knownEvidence := map[string]bool{}
	evidenceBytes := 0
	if len(in.CatalogEvidence) > maxCatalogEvidenceItems {
		return errors.New("too many scoped catalog evidence excerpts")
	}
	for _, e := range in.CatalogEvidence {
		if !catalogEvidenceAliasRE.MatchString(e.Alias) || knownEvidence[e.Alias] || !knownNamespace(e.Namespace) || !projectTagRE.MatchString(e.Tag) || !digestRE.MatchString(e.Digest) || len(e.Text) == 0 || len(e.Text) > 4*1024 {
			return errors.New("invalid scoped catalog evidence")
		}
		cardFound := false
		for _, card := range in.Catalog.Entries {
			if card.Namespace == e.Namespace && card.Tag == e.Tag && contextcatalog.Eligible(card) {
				cardFound = true
				break
			}
		}
		if !cardFound {
			return errors.New("catalog evidence references an unavailable card")
		}
		if e.Kind != "client_readme" && e.Kind != "client_agents" && e.Kind != "user_declared" {
			return errors.New("invalid scoped catalog evidence kind")
		}
		finger := sha256.Sum256([]byte(e.Text))
		if hex.EncodeToString(finger[:]) != e.Digest {
			return errors.New("scoped catalog evidence digest mismatch")
		}
		knownEvidence[e.Alias] = true
		evidenceBytes += len(e.Text)
	}
	if evidenceBytes > maxCatalogEvidenceBytes {
		return errors.New("scoped catalog evidence exceeds byte limit")
	}
	seen := map[string]bool{}
	for _, f := range in.Facts {
		if !requestAliasRE.MatchString(f.Alias) || seen[f.Alias] {
			return errors.New("invalid or duplicate maintenance alias")
		}
		seen[f.Alias] = true
		if strings.TrimSpace(f.Text) == "" || len(f.Text) > 8192 || strings.ContainsRune(f.Text, '\x00') {
			return errors.New("maintenance fact text is empty or exceeds limits")
		}
		if !knownNamespace(f.Namespace) {
			return errors.New("invalid maintenance fact namespace")
		}
		if !digestRE.MatchString(f.Fingerprint) {
			return errors.New("maintenance fact fingerprint is invalid")
		}
		if len(f.Tags) > 64 {
			return errors.New("maintenance fact has too many tags")
		}
		for _, tag := range f.Tags {
			if !projectTagRE.MatchString(tag) {
				return errors.New("maintenance fact has invalid tag")
			}
		}
		if f.PrimaryTag != "" && !projectTagRE.MatchString(f.PrimaryTag) {
			return errors.New("maintenance fact has invalid primary tag")
		}
	}
	return nil
}

func validateProposal(in Input, p Proposal) error {
	baseHash := in.baseCatalogHash
	if baseHash == "" {
		var err error
		baseHash, err = contextcatalog.Hash(in.Catalog)
		if err != nil {
			return err
		}
	}
	if p.SchemaVersion != 1 || p.BaseCatalogHash != baseHash || len(p.Grouping) > 100 || len(p.MissingEvidence) > 100 {
		return errors.New("maintenance proposal identity or size is invalid")
	}
	knownAliases := map[string]bool{}
	for _, f := range in.Facts {
		knownAliases[f.Alias] = true
	}
	knownCatalogEvidence := map[string]CatalogEvidence{}
	for _, evidence := range in.CatalogEvidence {
		knownCatalogEvidence[evidence.Alias] = evidence
	}
	approvedTags := map[string]bool{}
	for _, e := range in.Catalog.Entries {
		if e.Namespace == "projects" && contextcatalog.Eligible(e) {
			approvedTags[e.Tag] = true
		}
	}
	seen := map[string]bool{}
	for _, g := range p.Grouping {
		fact, exists := factForAlias(in.Facts, g.Alias)
		if !knownAliases[g.Alias] || !exists || fact.Namespace != "projects" || seen[g.Alias] {
			return errors.New("maintenance proposal contains unknown or duplicate alias")
		}
		seen[g.Alias] = true
		if g.PrimaryTag != "" && !approvedTags[g.PrimaryTag] {
			return errors.New("maintenance proposal contains unknown primary project tag")
		}
		if len(g.RelatedTags) > 32 || len(g.EvidenceAliases) > 100 {
			return errors.New("maintenance proposal exceeds grouping limits")
		}
		if len(g.EvidenceAliases) == 0 {
			return errors.New("maintenance grouping proposal requires supporting fact aliases")
		}
		seenTags := map[string]bool{}
		for _, tag := range g.RelatedTags {
			if !approvedTags[tag] || seenTags[tag] {
				return errors.New("maintenance proposal contains unknown or duplicate related tag")
			}
			seenTags[tag] = true
		}
		seenEvidence := map[string]bool{}
		for _, alias := range g.EvidenceAliases {
			if !knownAliases[alias] || seenEvidence[alias] {
				return errors.New("maintenance proposal contains unknown or duplicate evidence alias")
			}
			seenEvidence[alias] = true
		}
		if !seenEvidence[g.Alias] {
			return errors.New("maintenance grouping evidence must include its own fact alias")
		}
	}
	for _, code := range p.MissingEvidence {
		if !missingEvidenceCodes[code] {
			return errors.New("maintenance proposal contains an unknown missing-evidence code")
		}
	}
	if p.Catalog != nil {
		if err := contextcatalog.Validate(*p.Catalog); err != nil {
			return errors.New("maintenance catalog proposal is invalid")
		}
		if p.Catalog.ParentHash != baseHash {
			return errors.New("maintenance catalog proposal has wrong parent hash")
		}
		if len(p.Catalog.Entries) > contextcatalog.MaxEntries {
			return errors.New("maintenance catalog proposal exceeds entry limit")
		}
		for _, e := range p.Catalog.Entries {
			if e.ReviewStatus != "draft" && e.ReviewStatus != "unresolved" {
				return errors.New("maintenance catalog entries must remain unreviewed proposals")
			}
			if e.ProjectID != "" || e.ProjectKey != "" || e.Owner != "" || len(e.ClientEvidence) != 0 || e.DescriptionSource != "" || e.DeclaredSummary != "" {
				return errors.New("maintenance catalog proposal cannot set registry identity or provenance metadata")
			}
			for _, ref := range e.EvidenceRefs {
				if source, ok := knownCatalogEvidence[ref]; ok {
					if source.Namespace != e.Namespace || source.Tag != e.Tag {
						return errors.New("catalog evidence citation is scoped to another card")
					}
				} else if !knownAliases[ref] {
					return errors.New("maintenance catalog evidence must reference supplied fact aliases")
				}
			}
		}
	}
	return nil
}

func factForAlias(facts []Fact, alias string) (Fact, bool) {
	for _, f := range facts {
		if f.Alias == alias {
			return f, true
		}
	}
	return Fact{}, false
}

func knownNamespace(s string) bool {
	return s == "personal" || s == "work" || s == "projects" || s == "job-search" || s == "tech"
}
