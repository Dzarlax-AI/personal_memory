package aimaintenance

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

const maintenanceRelationshipRuleVersion = "maintenance-project-relationships-v2"

// maintenanceRelationshipInstructionsIdentity binds receipts to the actual
// relationship instructions sent to the provider, not only a config default.
func maintenanceRelationshipInstructionsIdentity() string {
	raw := maintenanceRelationshipRuleVersion + "\x00" + explicitSharedWithRules + "\x00" + directedUsesRules + "\x00" + registeredDirectoryRules
	sum := sha256.Sum256([]byte(raw))
	return maintenanceRelationshipRuleVersion + ":" + hex.EncodeToString(sum[:])
}

func registeredTagsForCards(cards []contextcatalog.Entry, cfg aipolicy.Config) map[string]bool {
	tags := make(map[string]bool, len(cards))
	if cfg.Maintenance.ProjectPolicy != "registered" {
		return tags
	}
	for _, entry := range cards {
		if entry.Namespace == "projects" && contextcatalog.Eligible(entry) && contextcatalog.RegisteredIdentity(entry) {
			tags[entry.Tag] = true
		}
	}
	return tags
}

// admittedCatalogEntries is the single maintenance-specific card admission
// boundary. It is evaluated against the currently loaded catalog on every run
// and at source revalidation boundaries. It intentionally does not affect
// fact egress.
func admittedCatalogEntries(snapshot contextcatalog.Snapshot, cfg aipolicy.Config) []contextcatalog.Entry {
	registeredTags := make(map[string]bool, len(snapshot.Entries))
	if cfg.Maintenance.ProjectPolicy == "registered" {
		for _, entry := range snapshot.Entries {
			if entry.Namespace == "projects" && contextcatalog.Eligible(entry) && contextcatalog.RegisteredIdentity(entry) {
				registeredTags[entry.Tag] = true
			}
		}
	}

	entries := make([]contextcatalog.Entry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !maintenanceAdmits(entry, cfg, registeredTags) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func maintenanceAdmits(entry contextcatalog.Entry, cfg aipolicy.Config, registeredTags map[string]bool) bool {
	if entry.Namespace != "projects" || !contextcatalog.Eligible(entry) {
		return false
	}
	if cfg.Maintenance.ProjectPolicy != "registered" {
		return cfg.Egress.Allows(entry.Namespace, []string{entry.Tag})
	}
	// Registered maintenance is operator-global; the active registry identity
	// binds owner/key internally, while only the public tag and original excerpts
	// reach the provider. Static grants cannot bypass registration identity.
	return contextcatalog.RegisteredIdentity(entry) && registeredTags[entry.Tag] &&
		containsString(cfg.Egress.AllowedNamespaces, entry.Namespace) &&
		!containsString(cfg.Egress.DeniedProjectTags, entry.Tag)
}

func maintenanceAdmittedCard(snapshot contextcatalog.Snapshot, cfg aipolicy.Config, namespace, tag string) bool {
	for _, entry := range admittedCatalogEntries(snapshot, cfg) {
		if entry.Namespace == namespace && entry.Tag == tag {
			return true
		}
	}
	return false
}
