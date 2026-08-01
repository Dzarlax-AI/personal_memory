package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

func normalizeReport(report Report) Report {
	report.TopK = append([]int(nil), report.TopK...)
	sort.Ints(report.TopK)
	report.Queries = append([]QueryReport(nil), report.Queries...)
	sort.Slice(report.Queries, func(i, j int) bool { return report.Queries[i].ID < report.Queries[j].ID })
	report.GateFailures = append([]string(nil), report.GateFailures...)
	sort.Strings(report.GateFailures)
	for i := range report.Queries {
		report.Queries[i].Results = append([]RetrievedItem(nil), report.Queries[i].Results...)
		if report.Queries[i].Lifecycle != nil {
			lifecycleReport := report.Queries[i].Lifecycle
			lifecycleReport.Candidates = append([]LifecycleCandidateReport(nil), lifecycleReport.Candidates...)
			sort.Slice(lifecycleReport.Candidates, func(i, j int) bool {
				return lifecycleReport.Candidates[i].ID < lifecycleReport.Candidates[j].ID
			})
			lifecycleReport.Violations = append([]string(nil), lifecycleReport.Violations...)
			sort.Strings(lifecycleReport.Violations)
			for j := range lifecycleReport.Candidates {
				lifecycleReport.Candidates[j].ReasonCodes =
					append([]LifecycleReasonCode(nil), lifecycleReport.Candidates[j].ReasonCodes...)
				sort.Slice(lifecycleReport.Candidates[j].ReasonCodes, func(a, b int) bool {
					return lifecycleReport.Candidates[j].ReasonCodes[a] <
						lifecycleReport.Candidates[j].ReasonCodes[b]
				})
			}
		}
	}
	if report.Lifecycle != nil {
		report.Lifecycle.Transitions = append([]TransitionReport(nil), report.Lifecycle.Transitions...)
		sort.Slice(report.Lifecycle.Transitions, func(i, j int) bool {
			return report.Lifecycle.Transitions[i].ID < report.Lifecycle.Transitions[j].ID
		})
		for i := range report.Lifecycle.Transitions {
			report.Lifecycle.Transitions[i].Violations =
				append([]string(nil), report.Lifecycle.Transitions[i].Violations...)
			sort.Strings(report.Lifecycle.Transitions[i].Violations)
		}
	}
	return report
}

// RenderJSON encodes a normalized report with deterministic ordering.
func RenderJSON(report Report) ([]byte, error) {
	report = normalizeReport(report)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode JSON report: %w", err)
	}
	return append(data, '\n'), nil
}

// RenderMarkdown renders the same normalized report for human review.
func RenderMarkdown(report Report) string {
	report = normalizeReport(report)
	var out strings.Builder
	fmt.Fprintf(&out, "# Retrieval evaluation: %s\n\n", report.DatasetVersion)
	fmt.Fprintf(&out, "- Mode: `%s`\n", report.Mode)
	fmt.Fprintf(&out, "- Configuration: `%s`\n", report.Configuration.Name)
	fmt.Fprintf(&out, "- Embedding: `%s@%s` (%s, %s, %d dimensions)\n", report.Embedding.ModelID, report.Embedding.ModelRevision, report.Embedding.DType, report.Embedding.Pooling, report.Embedding.VectorSize)
	fmt.Fprintf(&out, "- Gates: `%t`\n\n", report.GatesPassed)
	out.WriteString("## Aggregate\n\n")
	out.WriteString("| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&out, "| MRR | %.6f |\n", report.Aggregate.MRR)
	for _, k := range report.TopK {
		fmt.Fprintf(&out, "| Hit@%d | %.6f |\n", k, report.Aggregate.HitAt[k])
		fmt.Fprintf(&out, "| nDCG@%d | %.6f |\n", k, report.Aggregate.NDCGAt[k])
	}
	fmt.Fprintf(&out, "| Invariant violations | %d |\n", report.Aggregate.InvariantViolations)
	if report.Lifecycle != nil {
		out.WriteString("\n## Lifecycle\n\n")
		out.WriteString("| Metric | Value |\n| --- | ---: |\n")
		fmt.Fprintf(&out, "| Checks | %d |\n", report.Lifecycle.Aggregate.Checks)
		fmt.Fprintf(&out, "| Violations | %d |\n", report.Lifecycle.Aggregate.Violations)
		fmt.Fprintf(&out, "| Canonical preference checks | %d |\n", report.Lifecycle.Aggregate.CanonicalPreferenceChecks)
		fmt.Fprintf(&out, "| Canonical preference violations | %d |\n", report.Lifecycle.Aggregate.CanonicalPreferenceViolations)
		if len(report.Lifecycle.Transitions) > 0 {
			out.WriteString("\n### Transitions\n\n")
			out.WriteString("| Scenario | Valid | Reason | Passed |\n| --- | --- | --- | --- |\n")
			for _, transition := range report.Lifecycle.Transitions {
				fmt.Fprintf(&out, "| %s | %t | %s | %t |\n",
					escapeMarkdown(transition.ID), transition.Valid,
					transition.ReasonCode, transition.Passed)
			}
		}
	}
	if len(report.GateFailures) > 0 {
		out.WriteString("\n## Gate failures\n\n")
		for _, failure := range report.GateFailures {
			fmt.Fprintf(&out, "- %s\n", failure)
		}
	}
	out.WriteString("\n## Queries\n\n")
	out.WriteString("| Query | Target | Mode | Result IDs | MRR | Violations |\n| --- | --- | --- | --- | ---: | --- |\n")
	for _, query := range report.Queries {
		ids := make([]string, len(query.Results))
		for i, result := range query.Results {
			ids[i] = result.ID
		}
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %.6f | %s |\n",
			escapeMarkdown(query.ID), query.Target, query.Mode,
			escapeMarkdown(strings.Join(ids, ", ")), query.Metrics.MRR,
			escapeMarkdown(strings.Join(query.Metrics.InvariantViolations, ", ")))
	}
	var lifecycleQueries []QueryReport
	for _, query := range report.Queries {
		if query.Lifecycle != nil {
			lifecycleQueries = append(lifecycleQueries, query)
		}
	}
	if len(lifecycleQueries) > 0 {
		out.WriteString("\n### Lifecycle presentation\n\n")
		out.WriteString("| Query | Candidate | State | Decision | Reasons | Valid | Expired |\n")
		out.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
		for _, query := range lifecycleQueries {
			for _, candidate := range query.Lifecycle.Candidates {
				reasons := make([]string, len(candidate.ReasonCodes))
				for i, reason := range candidate.ReasonCodes {
					reasons[i] = string(reason)
				}
				fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %t | %t |\n",
					escapeMarkdown(query.ID), candidate.ID, candidate.State,
					candidate.Decision, strings.Join(reasons, ", "),
					candidate.Valid, candidate.Expired)
			}
		}
	}
	return out.String()
}

func escapeMarkdown(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, "|", `\|`)
}

// DecodeReport strictly decodes one report JSON document.
func DecodeReport(data []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Report{}, fmt.Errorf("decode report: trailing JSON")
		}
		return Report{}, fmt.Errorf("decode report trailing JSON: %w", err)
	}
	if (report.SchemaVersion != SchemaVersion && report.SchemaVersion != CurrentReportSchemaVersion) ||
		strings.TrimSpace(report.DatasetVersion) == "" {
		return Report{}, fmt.Errorf("report schema_version and dataset_version are invalid")
	}
	if report.SchemaVersion == SchemaVersion && report.Lifecycle != nil {
		return Report{}, fmt.Errorf("report lifecycle section requires schema_version %d", CurrentReportSchemaVersion)
	}
	if report.SchemaVersion == SchemaVersion {
		for _, query := range report.Queries {
			if query.Lifecycle != nil {
				return Report{}, fmt.Errorf("query lifecycle section requires schema_version %d", CurrentReportSchemaVersion)
			}
		}
	}
	if report.SchemaVersion == CurrentReportSchemaVersion && report.Lifecycle == nil {
		return Report{}, fmt.Errorf("schema_version %d report requires lifecycle section", CurrentReportSchemaVersion)
	}
	if report.SchemaVersion == CurrentReportSchemaVersion {
		if err := validateLifecycleReport(report); err != nil {
			return Report{}, fmt.Errorf("decode report lifecycle: %w", err)
		}
	}
	return normalizeReport(report), nil
}

func validateLifecycleReport(report Report) error {
	checks := 0
	violations := 0
	for _, query := range report.Queries {
		if query.Lifecycle == nil {
			continue
		}
		lifecycleReport := query.Lifecycle
		if !lifecycleReport.Intent.valid() {
			return fmt.Errorf("query %q has invalid intent", query.ID)
		}
		if lifecycleReport.Intent == QueryIntentAsOf {
			if !validISODate(lifecycleReport.AsOf) {
				return fmt.Errorf("query %q has invalid as_of", query.ID)
			}
		} else if lifecycleReport.AsOf != "" {
			return fmt.Errorf("query %q has unexpected as_of", query.ID)
		}
		if lifecycleReport.Checks < 0 {
			return fmt.Errorf("query %q has negative checks", query.ID)
		}
		checks += lifecycleReport.Checks
		violations += len(lifecycleReport.Violations)
		seen := make(map[string]struct{}, len(lifecycleReport.Candidates))
		for _, candidate := range lifecycleReport.Candidates {
			if err := validateNormalizedPointID(candidate.ID); err != nil {
				return fmt.Errorf("query %q candidate ID is invalid", query.ID)
			}
			if _, duplicate := seen[candidate.ID]; duplicate {
				return fmt.Errorf("query %q has duplicate candidate ID %q", query.ID, candidate.ID)
			}
			seen[candidate.ID] = struct{}{}
			if !candidate.Decision.valid() {
				return fmt.Errorf("query %q candidate %q has invalid decision", query.ID, candidate.ID)
			}
			if candidate.Valid {
				if !candidate.State.Valid() {
					return fmt.Errorf("query %q candidate %q has invalid state", query.ID, candidate.ID)
				}
			} else if candidate.State != "" {
				return fmt.Errorf("query %q invalid candidate %q exposes a state", query.ID, candidate.ID)
			}
			if len(candidate.ReasonCodes) == 0 {
				return fmt.Errorf("query %q candidate %q has no reason code", query.ID, candidate.ID)
			}
			for _, reason := range candidate.ReasonCodes {
				if !reason.validPresentation() {
					return fmt.Errorf("query %q candidate %q has unknown reason code", query.ID, candidate.ID)
				}
			}
		}
	}
	for _, transition := range report.Lifecycle.Transitions {
		checks++
		violations += len(transition.Violations)
		if strings.TrimSpace(transition.ID) == "" || transition.ID != strings.TrimSpace(transition.ID) {
			return fmt.Errorf("transition has invalid scenario ID")
		}
		if !transition.ReasonCode.validTransition() {
			return fmt.Errorf("transition %q has unknown reason code", transition.ID)
		}
		if transition.Valid != (transition.ReasonCode == ReasonTransitionValid) {
			return fmt.Errorf("transition %q has inconsistent validity and reason code", transition.ID)
		}
		if transition.Passed != (len(transition.Violations) == 0) {
			return fmt.Errorf("transition %q has inconsistent pass result", transition.ID)
		}
	}
	aggregate := report.Lifecycle.Aggregate
	if aggregate.Checks != checks || aggregate.Violations != violations {
		return fmt.Errorf("aggregate check or violation count is inconsistent")
	}
	if aggregate.CanonicalPreferenceChecks < 0 ||
		aggregate.CanonicalPreferenceViolations < 0 ||
		aggregate.CanonicalPreferenceViolations > aggregate.CanonicalPreferenceChecks {
		return fmt.Errorf("canonical preference counts are invalid")
	}
	return nil
}
