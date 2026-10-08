package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func (s *Server) registerProjectTool(srv *server.MCPServer) {
	a := s.aiState()
	if a == nil || !a.cfg.Registration.Active() {
		return
	}
	srv.AddTool(mcp.NewTool("ensure_project",
		mcp.WithDescription("Register bounded client-declared project context without inference. Registration is scoped to this deployment's configured memory owner. Repeating stable project_key is idempotent; changed descriptions create review proposals. Failure leaves ordinary memory usable. Excerpts are untrusted data; omit secrets, absolute paths and credential URLs."),
		mcp.WithRawOutputSchema(json.RawMessage(`{"type":"object","additionalProperties":false,"required":["status"],"properties":{"status":{"type":"string","enum":["created","existing","proposed_update","ambiguous","disabled","unavailable"]},"project_id":{"type":"string"},"tag":{"type":"string"},"catalog_hash":{"type":"string"}}}`)),
		mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithSchemaAdditionalProperties(false),
		mcp.WithString("project_key", mcp.Required(), mcp.Description("Stable credential-free repository identity digest or persisted local UUID; 8–128 ASCII characters")),
		mcp.WithString("namespace", mcp.Required(), mcp.Enum("projects")),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("tag", mcp.Required(), mcp.Description("Proposed lowercase kebab-case project tag")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Project purpose, maximum 2 KiB")),
		mcp.WithArray("evidence", mcp.Required(), mcp.MaxItems(4), mcp.Description("At most four excerpts, 4 KiB total; complete request at most 8 KiB"), mcp.Items(map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind", "text"}, "properties": map[string]any{
				"kind": map[string]any{"type": "string", "enum": []string{"client_readme", "client_agents", "user_declared"}}, "text": map[string]any{"type": "string"},
			},
		})),
	), s.ensureProject)
}

func registrationArguments(args map[string]interface{}) (contextcatalog.RegistrationInput, error) {
	raw, err := json.Marshal(args)
	if err != nil || len(raw) > contextcatalog.MaxRegistrationBytes {
		return contextcatalog.RegistrationInput{}, errors.New("project registration exceeds bounded request limit")
	}
	for _, key := range []string{"project_key", "namespace", "name", "tag", "summary"} {
		if _, ok := args[key].(string); !ok {
			return contextcatalog.RegistrationInput{}, errors.New("project registration fields must be strings")
		}
	}
	evidence, present := args["evidence"]
	if !present || evidence == nil {
		return contextcatalog.RegistrationInput{}, errors.New("project evidence must be an array")
	}
	evidenceRaw, err := json.Marshal(evidence)
	if err != nil || len(evidenceRaw) == 0 || evidenceRaw[0] != '[' {
		return contextcatalog.RegistrationInput{}, errors.New("project evidence must be an array")
	}
	var in contextcatalog.RegistrationInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		return in, errors.New("invalid project registration schema")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return in, errors.New("invalid project registration schema")
	}
	return in, nil
}

func (s *Server) ensureProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a := s.aiState()
	if a == nil || !a.cfg.Registration.Active() {
		return projectRegistrationResult(contextcatalog.RegistrationResult{Status: "disabled"}), nil
	}
	input, err := registrationArguments(req.GetArguments())
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := contextcatalog.ValidateRegistration(s.user, input); err != nil {
		return mcp.NewToolResultError("invalid or disallowed project registration input"), nil
	}
	if ctx.Err() != nil {
		return projectRegistrationResult(contextcatalog.RegistrationResult{Status: "unavailable"}), nil
	}
	result, err := contextcatalog.EnsureProject(a.cfg.CatalogDir, s.user, input)
	if err != nil {
		return projectRegistrationResult(contextcatalog.RegistrationResult{Status: "unavailable"}), nil
	}
	// Baseline cached reads also distinguish the new catalog generation when the
	// caller explicitly provides project context. No fact cache mutation is needed.
	return projectRegistrationResult(result), nil
}
func projectRegistrationResult(result contextcatalog.RegistrationResult) *mcp.CallToolResult {
	raw, _ := json.Marshal(result)
	return mcp.NewToolResultStructured(result, string(raw))
}

func (s *Server) resolveRecallProject(args map[string]interface{}, options *LifecycleRecallOptions) error {
	raw, supplied := args["project_context_id"]
	if !supplied {
		return nil
	}
	id, ok := raw.(string)
	if !ok || len(id) != 64 {
		return errors.New("invalid project_context_id")
	}
	a := s.aiState()
	if a == nil || a.cfg.CatalogDir == "" {
		return errors.New("project context unavailable; omit project_context_id to continue baseline memory")
	}
	entry, hash, err := contextcatalog.ResolveProject(a.cfg.CatalogDir, s.user, id)
	if err != nil || !contextcatalog.Eligible(entry) {
		return errors.New("project context unavailable; omit project_context_id to continue baseline memory")
	}
	safe := aijudgment.DescribeProject(entry)
	options.ProjectContext = &safe
	options.ProjectContextID = id
	options.CatalogHash = hash
	return nil
}
