package memory

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// FactSubject is a client declaration about the assertion, never its origin.
type FactSubject struct {
	Scope      string `json:"scope"`
	Context    string `json:"context,omitempty"`
	SourceKind string `json:"source_kind"`
}

func subjectArguments(args map[string]interface{}) (*FactSubject, error) {
	_, scopePresent := args["subject_scope"]
	_, contextPresent := args["subject_context"]
	if !scopePresent && !contextPresent {
		return nil, nil
	}
	for _, key := range []string{"subject_scope", "subject_context"} {
		if value, ok := args[key]; ok {
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	scope := strParam(args, "subject_scope")
	if scope == "" {
		scope = "unknown"
	}
	s := &FactSubject{Scope: scope, Context: strParam(args, "subject_context"), SourceKind: "client_declared"}
	return s, validateSubject(*s)
}

func validateSubject(s FactSubject) error {
	if !utf8.ValidString(s.Context) {
		return fmt.Errorf("subject_context must be valid UTF-8")
	}
	if s.Scope != "project" && s.Scope != "non_project" && s.Scope != "unknown" {
		return fmt.Errorf("subject_scope must be project, non_project, or unknown")
	}
	if s.SourceKind != "client_declared" {
		return fmt.Errorf("invalid subject source_kind")
	}
	return validateBoundedString("subject_context", s.Context, 2048, false)
}

func subjectPayload(payload map[string]interface{}) *FactSubject {
	raw, ok := payload["subject"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var s FactSubject
	if json.Unmarshal(b, &s) != nil || validateSubject(s) != nil {
		return nil
	}
	return &s
}

func subjectDecision(namespace string, s *FactSubject) string {
	if s == nil {
		return ""
	}
	if (s.Scope == "non_project" && namespace == "projects") || (s.Scope == "project" && namespace != "projects" && namespace != "work") {
		return "namespace_scope_mismatch"
	}
	if s.Scope == "non_project" || namespace == "personal" || namespace == "tech" || namespace == "job-search" {
		return "non_project"
	}
	return ""
}
