package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
)

const (
	maxLifecycleSourceBytes    = 255
	maxLifecycleReferenceBytes = 2048
	maxVerifiedAtBytes         = 64
	maxLifecycleRelations      = 100
	maxLifecyclePointIDBytes   = 255
	maxLifecycleMetadataBytes  = 64 << 10
)

var lifecycleArgumentKeys = []string{
	"lifecycle_state",
	"canonical",
	"provenance_source",
	"provenance_reference",
	"verified_at",
	"supersedes",
	"superseded_by",
}

type parsedLifecycleInput struct {
	Present bool
	Input   lifecycle.Input
}

func parseLifecycleInput(args map[string]interface{}, requireState bool) (parsedLifecycleInput, error) {
	var parsed parsedLifecycleInput
	for _, key := range lifecycleArgumentKeys {
		if value, exists := args[key]; exists && value != nil {
			parsed.Present = true
			break
		}
	}
	if !parsed.Present {
		if requireState {
			return parsed, fmt.Errorf("lifecycle_state is required")
		}
		return parsed, nil
	}

	state, err := lifecycleStringParam(args, "lifecycle_state")
	if err != nil {
		return parsed, err
	}
	if state == "" {
		if requireState {
			return parsed, fmt.Errorf("lifecycle_state is required")
		}
		state = string(lifecycle.Current)
	}
	parsed.Input.State = lifecycle.State(state)

	if raw, exists := args["canonical"]; exists && raw != nil {
		canonical, ok := raw.(bool)
		if !ok {
			return parsed, fmt.Errorf("canonical must be a boolean")
		}
		parsed.Input.Canonical = canonical
	}

	source, err := lifecycleStringParam(args, "provenance_source")
	if err != nil {
		return parsed, err
	}
	reference, err := lifecycleStringParam(args, "provenance_reference")
	if err != nil {
		return parsed, err
	}
	if err := validateBoundedString("provenance_source", source, maxLifecycleSourceBytes, false); err != nil {
		return parsed, err
	}
	if err := validateBoundedString("provenance_reference", reference, maxLifecycleReferenceBytes, false); err != nil {
		return parsed, err
	}
	if reference != "" && strings.TrimSpace(source) == "" {
		return parsed, fmt.Errorf("provenance_reference requires provenance_source")
	}
	if strings.TrimSpace(source) != "" {
		parsed.Input.Provenance = &lifecycle.Provenance{Source: source, Reference: reference}
	}

	parsed.Input.VerifiedAt, err = lifecycleStringParam(args, "verified_at")
	if err != nil {
		return parsed, err
	}
	if err := validateBoundedString("verified_at", parsed.Input.VerifiedAt, maxVerifiedAtBytes, false); err != nil {
		return parsed, err
	}

	if parsed.Input.Supersedes, err = lifecycleRelationshipParam(args, "supersedes"); err != nil {
		return parsed, err
	}
	if parsed.Input.SupersededBy, err = lifecycleRelationshipParam(args, "superseded_by"); err != nil {
		return parsed, err
	}

	encoded, err := json.Marshal(parsed.Input)
	if err != nil {
		return parsed, fmt.Errorf("encode lifecycle metadata: %w", err)
	}
	if len(encoded) > maxLifecycleMetadataBytes {
		return parsed, fmt.Errorf("lifecycle metadata must be at most %d bytes", maxLifecycleMetadataBytes)
	}
	return parsed, nil
}

func lifecycleStringParam(args map[string]interface{}, key string) (string, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return value, nil
}

func lifecycleRelationshipParam(args map[string]interface{}, key string) ([]string, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return []string{}, nil
	}
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = append(values, typed...)
	case []interface{}:
		if len(typed) > maxLifecycleRelations {
			return nil, fmt.Errorf("%s must contain at most %d entries", key, maxLifecycleRelations)
		}
		values = make([]string, 0, len(typed))
		for i, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", key, i)
			}
			values = append(values, value)
		}
	default:
		return nil, fmt.Errorf("%s must be an array of point ID strings", key)
	}
	if len(values) > maxLifecycleRelations {
		return nil, fmt.Errorf("%s must contain at most %d entries", key, maxLifecycleRelations)
	}
	for i, value := range values {
		if err := validateBoundedString(fmt.Sprintf("%s[%d]", key, i), value, maxLifecyclePointIDBytes, true); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func lifecycleMutationPayload(view lifecycle.View) (map[string]interface{}, []string) {
	set := lifecycle.PayloadFromView(view)
	deleteKeys := make([]string, 0, 2)
	for _, key := range []string{"provenance", "verified_at"} {
		if _, exists := set[key]; !exists {
			deleteKeys = append(deleteKeys, key)
		}
	}
	return set, deleteKeys
}
