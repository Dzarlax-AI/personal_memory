package eval

import "fmt"

// ExperimentOverrides contains only dimensions intended to vary between
// evaluation experiments. Nil fields preserve the dataset value.
type ExperimentOverrides struct {
	ConfigurationName   *string
	InputProfile        *InputProfile
	RetrievalStrategy   *RetrievalStrategy
	DenseCandidateLimit *int
	RRFConstant         *int
}

// WithExperimentOverrides returns an independently mutable dataset copy and
// validates the resulting experiment through the normal source contract.
func WithExperimentOverrides(source *Dataset, overrides ExperimentOverrides, runSource string) (*Dataset, error) {
	if source == nil {
		return nil, fmt.Errorf("dataset is required")
	}
	cloned := cloneDataset(source)
	cloned.Configuration.present = clonePresence(source.Configuration.present)
	if cloned.Configuration.present == nil {
		cloned.Configuration.present = make(map[string]bool)
	}
	if overrides.ConfigurationName != nil {
		cloned.Configuration.Name = *overrides.ConfigurationName
	}
	if overrides.InputProfile != nil {
		cloned.Embedding.InputProfile = *overrides.InputProfile
		cloned.Embedding.inputProfilePresent = true
	}
	if overrides.RetrievalStrategy != nil {
		cloned.Configuration.RetrievalStrategy = *overrides.RetrievalStrategy
		cloned.Configuration.present["retrieval_strategy"] = true
	}
	if overrides.DenseCandidateLimit != nil {
		cloned.Configuration.DenseCandidateLimit = *overrides.DenseCandidateLimit
		cloned.Configuration.present["dense_candidate_limit"] = true
	}
	if overrides.RRFConstant != nil {
		cloned.Configuration.RRFConstant = *overrides.RRFConstant
		cloned.Configuration.present["rrf_constant"] = true
	}
	if err := cloned.ValidateForSource(runSource); err != nil {
		return nil, err
	}
	return &cloned, nil
}
