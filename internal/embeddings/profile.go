package embeddings

import (
	"errors"
	"fmt"
	"strings"
)

const multilingualE5SmallModelID = "intfloat/multilingual-e5-small"

// InputProfile identifies the versioned text transformation applied before
// sending an input to the embedding model.
type InputProfile string

const (
	LegacyRawV1      InputProfile = "legacy-raw-v1"
	MultilingualE5V1 InputProfile = "multilingual-e5-v1"
)

// ErrReservedInputPrefix means a profile-owned prefix was found at the start
// of raw input, making it ambiguous whether the profile was already applied.
var ErrReservedInputPrefix = errors.New("reserved embedding input prefix")

// Purpose identifies how an embedding input will be used. It is deliberately
// typed so retrieval and passage call sites cannot rely on ad-hoc strings.
type Purpose uint8

const (
	RetrievalQuery Purpose = iota + 1
	FactPassage
	ChunkPassage
	FolderPassage
)

// NormalizeInputProfile maps the zero value to the compatibility profile used
// before profiles were persisted.
func NormalizeInputProfile(profile InputProfile) InputProfile {
	if profile == "" {
		return LegacyRawV1
	}
	return profile
}

// ValidateInputProfile verifies that a profile is known and compatible with
// the configured model.
func ValidateInputProfile(profile InputProfile, modelID string) error {
	profile = NormalizeInputProfile(profile)
	modelID = strings.TrimSpace(modelID)
	switch profile {
	case LegacyRawV1:
		return nil
	case MultilingualE5V1:
		if modelID != multilingualE5SmallModelID {
			return fmt.Errorf("embedding input profile %q does not support model %q", profile, modelID)
		}
		return nil
	default:
		return fmt.Errorf("unknown embedding input profile %q", profile)
	}
}

// TransformInput applies a profile to raw, literal user or document text.
// Callers must not add model prefixes themselves. Prefix-looking text in the
// raw value is content and is therefore preserved rather than stripped.
func TransformInput(rawText string, purpose Purpose, profile InputProfile, modelID string) (string, error) {
	if err := validatePurpose(purpose); err != nil {
		return "", err
	}
	profile = NormalizeInputProfile(profile)
	if err := ValidateInputProfile(profile, modelID); err != nil {
		return "", err
	}

	switch profile {
	case LegacyRawV1:
		return rawText, nil
	case MultilingualE5V1:
		if strings.HasPrefix(rawText, "query: ") || strings.HasPrefix(rawText, "passage: ") {
			return "", fmt.Errorf("%w for profile %q", ErrReservedInputPrefix, profile)
		}
		if purpose == RetrievalQuery {
			return "query: " + rawText, nil
		}
		return "passage: " + rawText, nil
	default:
		return "", fmt.Errorf("unknown embedding input profile %q", profile)
	}
}

func validatePurpose(purpose Purpose) error {
	switch purpose {
	case RetrievalQuery, FactPassage, ChunkPassage, FolderPassage:
		return nil
	default:
		return fmt.Errorf("unknown embedding purpose %d", purpose)
	}
}
