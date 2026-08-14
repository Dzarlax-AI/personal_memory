package maintenance

import (
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	Active      Status = "active"
	Quarantined Status = "quarantined"
)

var quarantineReasons = map[string]struct{}{
	"expired": {}, "superseded_retention": {}, "operator_selected": {},
}

type View struct {
	Status            Status `json:"status"`
	Legacy            bool   `json:"legacy"`
	Valid             bool   `json:"valid"`
	InvalidReason     string `json:"invalid_reason,omitempty"`
	QuarantinedAt     string `json:"quarantined_at,omitempty"`
	QuarantineReason  string `json:"quarantine_reason,omitempty"`
	QuarantineBatchID string `json:"quarantine_batch_id,omitempty"`
}

func Parse(payload map[string]interface{}) View {
	_, hasStatus := payload["maintenance_status"]
	_, hasAt := payload["quarantined_at"]
	_, hasReason := payload["quarantine_reason"]
	_, hasBatch := payload["quarantine_batch_id"]
	if !hasStatus && !hasAt && !hasReason && !hasBatch {
		return View{Status: Active, Legacy: true, Valid: true}
	}
	status, ok := payload["maintenance_status"].(string)
	if !ok || strings.TrimSpace(status) != status || (Status(status) != Active && Status(status) != Quarantined) {
		return invalid("maintenance_status must be active or quarantined")
	}
	view := View{Status: Status(status), Valid: true}
	if view.Status == Active {
		if hasAt || hasReason || hasBatch {
			return invalid("active maintenance status cannot include quarantine metadata")
		}
		return view
	}
	view.QuarantinedAt, ok = payload["quarantined_at"].(string)
	if !ok {
		return invalid("quarantined_at must use RFC3339 format")
	}
	if _, err := time.Parse(time.RFC3339, view.QuarantinedAt); err != nil {
		return invalid("quarantined_at must use RFC3339 format")
	}
	view.QuarantineReason, ok = payload["quarantine_reason"].(string)
	if !ok || strings.TrimSpace(view.QuarantineReason) == "" || strings.TrimSpace(view.QuarantineReason) != view.QuarantineReason {
		return invalid("quarantine_reason must be non-empty")
	}
	if _, known := quarantineReasons[view.QuarantineReason]; !known {
		return invalid("quarantine_reason is not supported")
	}
	view.QuarantineBatchID, ok = payload["quarantine_batch_id"].(string)
	if !ok || strings.TrimSpace(view.QuarantineBatchID) == "" || strings.TrimSpace(view.QuarantineBatchID) != view.QuarantineBatchID {
		return invalid("quarantine_batch_id must be non-empty")
	}
	return view
}

func invalid(reason string) View { return View{Valid: false, InvalidReason: reason} }

func IsActive(payload map[string]interface{}) bool {
	view := Parse(payload)
	return view.Valid && view.Status == Active
}

func ActiveFilter(base map[string]interface{}) map[string]interface{} {
	active := map[string]interface{}{"should": []map[string]interface{}{
		{"key": "maintenance_status", "match": map[string]interface{}{"value": string(Active)}},
		{"is_empty": map[string]interface{}{"key": "maintenance_status"}},
	}}
	if len(base) == 0 {
		return active
	}
	return map[string]interface{}{"must": []interface{}{clone(base), active}}
}

func clone(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			out[key] = clone(nested)
		}
		return out
	case []map[string]interface{}:
		out := make([]map[string]interface{}, len(typed))
		for index, nested := range typed {
			out[index] = clone(nested).(map[string]interface{})
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for index, nested := range typed {
			out[index] = clone(nested)
		}
		return out
	default:
		return typed
	}
}

func ValidateOptions(options Options) error {
	if strings.TrimSpace(options.Collection) == "" {
		return fmt.Errorf("collection is required")
	}
	if options.ReferenceTime.IsZero() {
		return fmt.Errorf("reference time is required")
	}
	if options.SupersededRetention <= 0 || options.StaleAfter <= 0 {
		return fmt.Errorf("retention durations must be positive")
	}
	if options.LowRecallThreshold < 0 {
		return fmt.Errorf("low recall threshold must not be negative")
	}
	return nil
}
