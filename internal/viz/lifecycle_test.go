package viz

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
)

func TestLifecycleDTOHidesReferencesFromSummariesAndRedactsUnsafeDetailReferences(t *testing.T) {
	view := lifecycle.View{
		State: lifecycle.Current, Valid: true,
		Provenance: &lifecycle.Provenance{Source: "import", Reference: "/Users/alexey/private.md"},
	}
	summary := lifecycleSummaryDTO(view)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["provenance"], map[string]interface{}{
		"source": "import", "source_present": true, "has_reference": true, "reference_redacted": true,
	}) {
		t.Fatalf("summary provenance = %#v", got["provenance"])
	}

	detail := lifecycleDetailDTO(view)
	if detail.Provenance == nil || detail.Provenance.Reference != "" || !detail.Provenance.ReferenceRedacted {
		t.Fatalf("detail provenance = %#v, want redacted path", detail.Provenance)
	}
	view.Provenance.Reference = "decision-7"
	detail = lifecycleDetailDTO(view)
	if detail.Provenance.Reference != "decision-7" || detail.Provenance.ReferenceRedacted {
		t.Fatalf("safe detail provenance = %#v", detail.Provenance)
	}
}

func TestPrivacySafeReferenceRejectsPathAndOversizedForms(t *testing.T) {
	long := make([]byte, maxLifecycleReferenceBytes+1)
	for i := range long {
		long[i] = 'x'
	}
	for _, value := range []string{"/etc/passwd", `C:\Users\me\secret`, `\\server\share\secret`, "file:///tmp/secret", string(long)} {
		if _, ok := privacySafeReference(value); ok {
			t.Errorf("privacySafeReference(%q) accepted unsafe value", value)
		}
	}
	if got, ok := privacySafeReference("ticket-31"); !ok || got != "ticket-31" {
		t.Fatalf("safe reference = %q, %v", got, ok)
	}
}

func TestLifecycleDTOConservativelyRedactsUnsafeProvenanceFields(t *testing.T) {
	longSource := strings.Repeat("s", maxLifecycleSourceBytes+1)
	longReference := strings.Repeat("r", maxLifecycleReferenceBytes+1)
	unsafeValues := []string{
		"/etc/passwd", `C:\Users\me\secret`, `\\server\share\secret`, "file:///tmp/secret",
		"../private/note.md", `folder\..\private`, "%2e%2e/private", "~/private", "line\nbreak",
		"https://user:password@example.com/private", "//token@example.com/private", longSource,
	}
	for _, value := range unsafeValues {
		t.Run(value, func(t *testing.T) {
			reference := value
			if value == longSource {
				reference = longReference
			}
			view := lifecycle.View{
				State: lifecycle.Current, Valid: true,
				Provenance: &lifecycle.Provenance{Source: value, Reference: reference},
			}
			summary := lifecycleSummaryDTO(view)
			if summary.Provenance == nil || summary.Provenance.Source != "" || !summary.Provenance.SourcePresent || !summary.Provenance.SourceRedacted {
				t.Fatalf("summary provenance = %#v", summary.Provenance)
			}
			detail := lifecycleDetailDTO(view)
			if detail.Provenance == nil || detail.Provenance.Source != "" || detail.Provenance.Reference != "" ||
				!detail.Provenance.SourceRedacted || !detail.Provenance.ReferenceRedacted {
				t.Fatalf("detail provenance = %#v", detail.Provenance)
			}
			encoded, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), value) {
				t.Fatalf("unsafe value leaked: %s", encoded)
			}
		})
	}
	view := lifecycle.View{State: lifecycle.Current, Valid: true, Provenance: &lifecycle.Provenance{Source: "import", Reference: longReference}}
	detail := lifecycleDetailDTO(view)
	if detail.Provenance.Source != "import" || detail.Provenance.SourceRedacted || detail.Provenance.Reference != "" || !detail.Provenance.ReferenceRedacted {
		t.Fatalf("mixed safe/unsafe provenance = %#v", detail.Provenance)
	}
}
