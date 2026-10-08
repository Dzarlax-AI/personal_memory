package integrationbundle

import "regexp"

var (
	projectWorkflowTag  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	projectWorkflowID   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	projectWorkflowHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ProjectRegistrationDecision describes the client-side prerequisites. The
// registration tool is optional and is discovered per session.
type ProjectRegistrationDecision struct {
	ExplicitProjectContext bool
	ToolAvailable          bool
	StableKeyPersistable   bool
}

// ShouldEnsureProject is the shared client contract for deciding whether to
// make the separate, pre-read registration call.
func ShouldEnsureProject(in ProjectRegistrationDecision) bool {
	return in.ExplicitProjectContext && in.ToolAvailable && in.StableKeyPersistable
}

// ProjectRegistrationResult is the identity-bearing portion of ensure_project.
type ProjectRegistrationResult struct {
	Status      string
	ProjectID   string
	Tag         string
	CatalogHash string
}

// ProjectRegistrationIdentity accepts only a complete successful server
// result. The caller may use project_id for project_context_id and tag for
// source_project; these fields do not determine the fact's primary_tag.
func ProjectRegistrationIdentity(result ProjectRegistrationResult) (projectID, tag string, ok bool) {
	switch result.Status {
	case "created", "existing":
	default:
		return "", "", false
	}
	if !projectWorkflowID.MatchString(result.ProjectID) || !projectWorkflowTag.MatchString(result.Tag) || !projectWorkflowHash.MatchString(result.CatalogHash) {
		return "", "", false
	}
	return result.ProjectID, result.Tag, true
}
