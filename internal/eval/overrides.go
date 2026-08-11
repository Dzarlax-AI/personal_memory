package eval

import "fmt"

// ExperimentOverrides contains only dimensions intended to vary between
// evaluation experiments. Nil fields preserve the dataset value.
type ExperimentOverrides struct {
	ConfigurationName       *string
	InputProfile            *InputProfile
	RetrievalStrategy       *RetrievalStrategy
	DenseCandidateLimit     *int
	RRFConstant             *int
	DocumentRoutingStrategy *DocumentRoutingStrategy
	RoutingCandidateLimit   *int
	RoutingRRFConstant      *int
	RerankerModelID         *string
	RerankerCandidateCap    *int
	RerankerTimeoutMS       *int
}

// WithExperimentOverrides returns an independently mutable dataset copy and
// validates the resulting experiment through the normal source contract.
func WithExperimentOverrides(source *Dataset, overrides ExperimentOverrides, runSource string) (*Dataset, error) {
	if source == nil {
		return nil, fmt.Errorf("dataset is required")
	}
	cloned, err := cloneDataset(source)
	if err != nil {
		return nil, fmt.Errorf("clone dataset: %w", err)
	}
	cloned.Configuration.present = clonePresence(source.Configuration.present)
	if cloned.Configuration.present == nil {
		cloned.Configuration.present = make(map[string]bool)
	}
	if overrides.ConfigurationName != nil {
		cloned.Configuration.Name = *overrides.ConfigurationName
	}
	if overrides.InputProfile != nil {
		if runSource == "fixture" {
			return nil, fmt.Errorf("input profile override cannot relabel precomputed fixture vectors")
		}
		profileChanged := cloned.Embedding.InputProfile != *overrides.InputProfile
		cloned.Embedding.InputProfile = *overrides.InputProfile
		cloned.Embedding.inputProfilePresent = true
		if runSource == "live" && profileChanged {
			for i := range cloned.Queries {
				cloned.Queries[i].Vector = nil
			}
		}
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
	if overrides.DocumentRoutingStrategy != nil {
		cloned.Configuration.DocumentRoutingStrategy = *overrides.DocumentRoutingStrategy
		cloned.Configuration.present["document_routing_strategy"] = true
	}
	if overrides.RoutingCandidateLimit != nil {
		cloned.Configuration.RoutingCandidateLimit = *overrides.RoutingCandidateLimit
		cloned.Configuration.present["routing_candidate_limit"] = true
	}
	if overrides.RoutingRRFConstant != nil {
		cloned.Configuration.RoutingRRFConstant = *overrides.RoutingRRFConstant
		cloned.Configuration.present["routing_rrf_constant"] = true
	}
	if overrides.RerankerModelID != nil {
		cloned.Configuration.RerankerModelID = *overrides.RerankerModelID
		cloned.Configuration.present["reranker_model_id"] = true
	}
	if overrides.RerankerCandidateCap != nil {
		cloned.Configuration.RerankerCandidateCap = *overrides.RerankerCandidateCap
		cloned.Configuration.present["reranker_candidate_cap"] = true
	}
	if overrides.RerankerTimeoutMS != nil {
		cloned.Configuration.RerankerTimeoutMS = *overrides.RerankerTimeoutMS
		cloned.Configuration.present["reranker_timeout_ms"] = true
	}
	if err := cloned.ValidateForSource(runSource); err != nil {
		return nil, err
	}
	return &cloned, nil
}
