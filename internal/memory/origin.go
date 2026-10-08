package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FactOrigin records a declaration of source, not ownership or truth.
type FactOrigin struct {
	SourceProject string `json:"source_project"`
	SourceKind    string `json:"source_kind"`
	RecordedAt    string `json:"recorded_at"`
}

func originArguments(args map[string]interface{}) (*FactOrigin, error) {
	project := strParam(args, "source_project")
	kind := strParam(args, "source_kind")
	if project == "" && kind == "" {
		return nil, nil
	}
	if err := validateBoundedString("source_project", project, maxTagBytes, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(project) != project {
		return nil, fmt.Errorf("source_project must not contain surrounding whitespace")
	}
	if kind != "user_declared" && kind != "client_declared" {
		return nil, fmt.Errorf("source_kind must be user_declared or client_declared")
	}
	return &FactOrigin{SourceProject: project, SourceKind: kind, RecordedAt: nowISO()}, nil
}
func originPayload(payload map[string]interface{}) *FactOrigin {
	raw, ok := payload["origin"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var o FactOrigin
	if json.Unmarshal(b, &o) != nil || validateOrigin(o) != nil {
		return nil
	}
	return &o
}
func validateOrigin(o FactOrigin) error {
	if err := validateBoundedString("source_project", o.SourceProject, maxTagBytes, true); err != nil {
		return err
	}
	if o.SourceKind != "user_declared" && o.SourceKind != "client_declared" {
		return fmt.Errorf("invalid source_kind")
	}
	if _, err := time.Parse(time.RFC3339, o.RecordedAt); err != nil {
		return fmt.Errorf("invalid origin timestamp")
	}
	return nil
}
func importOrigin(payload map[string]interface{}) error {
	if _, ok := payload["origin"]; !ok {
		return nil
	}
	if originPayload(payload) == nil {
		return fmt.Errorf("invalid origin metadata")
	}
	return nil
}
func formatOrigin(o *FactOrigin) string {
	if o == nil {
		return ""
	}
	return fmt.Sprintf(" [origin:%s kind:%s recorded:%s]", o.SourceProject, o.SourceKind, o.RecordedAt)
}
