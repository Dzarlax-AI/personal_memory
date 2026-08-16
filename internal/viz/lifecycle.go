package viz

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
)

const (
	maxLifecycleSourceBytes    = 256
	maxLifecycleReferenceBytes = 512
)

var windowsAbsolutePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// lifecycleDTO is the Viz API representation of lifecycle metadata. It is
// intentionally separate from lifecycle.View, which mirrors storage-facing
// metadata and may contain a provenance reference unsuitable for list views.
type lifecycleDTO struct {
	State          string                  `json:"state"`
	Legacy         bool                    `json:"legacy"`
	Canonical      bool                    `json:"canonical"`
	Provenance     *lifecycleProvenanceDTO `json:"provenance,omitempty"`
	VerifiedAt     string                  `json:"verified_at,omitempty"`
	TransitionedAt string                  `json:"transitioned_at,omitempty"`
	Supersedes     []string                `json:"supersedes"`
	SupersededBy   []string                `json:"superseded_by"`
	Valid          bool                    `json:"valid"`
	InvalidReason  string                  `json:"invalid_reason,omitempty"`
}

type lifecycleProvenanceDTO struct {
	Source            string `json:"source,omitempty"`
	SourcePresent     bool   `json:"source_present"`
	SourceRedacted    bool   `json:"source_redacted,omitempty"`
	HasReference      bool   `json:"has_reference"`
	Reference         string `json:"reference,omitempty"`
	ReferenceRedacted bool   `json:"reference_redacted,omitempty"`
}

func lifecycleSummaryDTO(view lifecycle.View) lifecycleDTO {
	return lifecycleDTOFromView(view, false)
}

func lifecycleDetailDTO(view lifecycle.View) lifecycleDTO {
	return lifecycleDTOFromView(view, true)
}

func lifecycleDTOFromView(view lifecycle.View, includeSafeReference bool) lifecycleDTO {
	state := string(view.State)
	if !view.Valid {
		state = "invalid"
	}
	result := lifecycleDTO{
		State: state, Legacy: view.Legacy, Canonical: view.Canonical,
		VerifiedAt: view.VerifiedAt, TransitionedAt: view.TransitionedAt,
		Supersedes: sortedUniqueIDs(view.Supersedes), SupersededBy: sortedUniqueIDs(view.SupersededBy),
		Valid: view.Valid, InvalidReason: safeInvalidReason(view.InvalidReason),
	}
	if view.Provenance != nil {
		result.Provenance = &lifecycleProvenanceDTO{
			SourcePresent:     true,
			HasReference:      view.Provenance.Reference != "",
			ReferenceRedacted: !includeSafeReference && view.Provenance.Reference != "",
		}
		if source, ok := privacySafeProvenanceValue(view.Provenance.Source, maxLifecycleSourceBytes); ok {
			result.Provenance.Source = source
		} else {
			result.Provenance.SourceRedacted = true
		}
		if includeSafeReference && view.Provenance.Reference != "" {
			if reference, ok := privacySafeReference(view.Provenance.Reference); ok {
				result.Provenance.Reference = reference
			} else {
				result.Provenance.ReferenceRedacted = true
			}
		}
	}
	return result
}

func safeInvalidReason(reason string) string {
	if reason == "" {
		return ""
	}
	// Parse emits fixed metadata-only validation errors. Keep a defensive bound
	// so a future storage-derived error cannot turn this field into a data leak.
	if len(reason) > 256 || !utf8.ValidString(reason) {
		return "invalid lifecycle metadata"
	}
	return reason
}

func privacySafeReference(reference string) (string, bool) {
	return privacySafeProvenanceValue(reference, maxLifecycleReferenceBytes)
}

func privacySafeProvenanceValue(value string, maxBytes int) (string, bool) {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return "", false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	if unsafePathLikeProvenance(value) {
		return "", false
	}
	if decoded, err := url.PathUnescape(value); err == nil && decoded != value && unsafePathLikeProvenance(decoded) {
		return "", false
	}
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		return "", false
	}
	return value, true
}

func unsafePathLikeProvenance(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) ||
		windowsAbsolutePath.MatchString(value) || strings.HasPrefix(lower, "file:") ||
		strings.HasPrefix(value, "~") || containsParentTraversal(value)
}

func containsParentTraversal(value string) bool {
	normalized := strings.ReplaceAll(value, `\`, "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func sortedUniqueIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
