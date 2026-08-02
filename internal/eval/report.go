package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
)

func normalizeReport(report Report) Report {
	report.TopK = append([]int(nil), report.TopK...)
	sort.Ints(report.TopK)
	if report.SchemaVersion == CurrentReportSchemaVersion && report.Queries != nil {
		report.Queries = append([]QueryReport{}, report.Queries...)
	} else {
		report.Queries = append([]QueryReport(nil), report.Queries...)
	}
	for i := range report.Queries {
		report.Queries[i].Cohorts = append([]QueryCohort(nil), report.Queries[i].Cohorts...)
		sort.Slice(report.Queries[i].Cohorts, func(a, b int) bool {
			return report.Queries[i].Cohorts[a] < report.Queries[i].Cohorts[b]
		})
		report.Queries[i].Lifecycle = cloneQueryLifecycleReport(report.Queries[i].Lifecycle)
	}
	if report.SchemaVersion == CurrentReportSchemaVersion && report.Cohorts != nil {
		report.Cohorts = append([]CohortAggregateMetrics{}, report.Cohorts...)
	} else {
		report.Cohorts = append([]CohortAggregateMetrics(nil), report.Cohorts...)
	}
	for i := range report.Cohorts {
		report.Cohorts[i].HitAt = cloneMetricMap(report.Cohorts[i].HitAt)
		report.Cohorts[i].NDCGAt = cloneMetricMap(report.Cohorts[i].NDCGAt)
		report.Cohorts[i].present = clonePresence(report.Cohorts[i].present)
	}
	sort.Slice(report.Cohorts, func(i, j int) bool {
		return report.Cohorts[i].Cohort < report.Cohorts[j].Cohort
	})
	report.Lifecycle = cloneLifecycleReport(report.Lifecycle)
	sort.Slice(report.Queries, func(i, j int) bool { return report.Queries[i].ID < report.Queries[j].ID })
	report.GateFailures = append([]string(nil), report.GateFailures...)
	sort.Strings(report.GateFailures)
	for i := range report.Queries {
		if report.SchemaVersion == CurrentReportSchemaVersion && report.Queries[i].Results != nil {
			report.Queries[i].Results = append([]RetrievedItem{}, report.Queries[i].Results...)
		} else {
			report.Queries[i].Results = append([]RetrievedItem(nil), report.Queries[i].Results...)
		}
		if report.Queries[i].Lifecycle != nil {
			lifecycleReport := report.Queries[i].Lifecycle
			lifecycleReport.Candidates = append([]LifecycleCandidateReport{}, lifecycleReport.Candidates...)
			sort.Slice(lifecycleReport.Candidates, func(i, j int) bool {
				return lifecycleReport.Candidates[i].ID < lifecycleReport.Candidates[j].ID
			})
			lifecycleReport.Violations = append([]LifecycleViolation{}, lifecycleReport.Violations...)
			sortLifecycleViolations(lifecycleReport.Violations)
			for j := range lifecycleReport.Candidates {
				lifecycleReport.Candidates[j].ReasonCodes =
					append([]LifecycleReasonCode{}, lifecycleReport.Candidates[j].ReasonCodes...)
				sort.Slice(lifecycleReport.Candidates[j].ReasonCodes, func(a, b int) bool {
					return lifecycleReport.Candidates[j].ReasonCodes[a] <
						lifecycleReport.Candidates[j].ReasonCodes[b]
				})
			}
		}
	}
	if report.Lifecycle != nil {
		report.Lifecycle.Transitions = append([]TransitionReport{}, report.Lifecycle.Transitions...)
		sort.Slice(report.Lifecycle.Transitions, func(i, j int) bool {
			return report.Lifecycle.Transitions[i].ID < report.Lifecycle.Transitions[j].ID
		})
		for i := range report.Lifecycle.Transitions {
			report.Lifecycle.Transitions[i].Violations =
				append([]LifecycleViolation{}, report.Lifecycle.Transitions[i].Violations...)
			sortLifecycleViolations(report.Lifecycle.Transitions[i].Violations)
		}
	}
	return report
}

func cloneMetricMap(source map[int]float64) map[int]float64 {
	if source == nil {
		return nil
	}
	cloned := make(map[int]float64, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneQueryLifecycleReport(source *QueryLifecycleReport) *QueryLifecycleReport {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.present = clonePresence(source.present)
	cloned.Candidates = append([]LifecycleCandidateReport{}, source.Candidates...)
	for i := range cloned.Candidates {
		cloned.Candidates[i].ReasonCodes =
			append([]LifecycleReasonCode{}, source.Candidates[i].ReasonCodes...)
		cloned.Candidates[i].present = clonePresence(source.Candidates[i].present)
	}
	cloned.Violations = append([]LifecycleViolation{}, source.Violations...)
	for i := range cloned.Violations {
		cloned.Violations[i].present = clonePresence(source.Violations[i].present)
	}
	return &cloned
}

func cloneLifecycleReport(source *LifecycleReport) *LifecycleReport {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.present = clonePresence(source.present)
	cloned.Aggregate.present = clonePresence(source.Aggregate.present)
	cloned.Transitions = append([]TransitionReport{}, source.Transitions...)
	for i := range cloned.Transitions {
		cloned.Transitions[i].present = clonePresence(source.Transitions[i].present)
		cloned.Transitions[i].Violations =
			append([]LifecycleViolation{}, source.Transitions[i].Violations...)
		for j := range cloned.Transitions[i].Violations {
			cloned.Transitions[i].Violations[j].present =
				clonePresence(source.Transitions[i].Violations[j].present)
		}
	}
	return &cloned
}

func clonePresence(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
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
	if report.SchemaVersion == CurrentReportSchemaVersion {
		fmt.Fprintf(&out, "- Input profile: `%s`\n", report.Embedding.InputProfile)
		fmt.Fprintf(&out, "- Retrieval strategy: `%s`\n", report.Configuration.RetrievalStrategy)
	}
	fmt.Fprintf(&out, "- Gates: `%t`\n\n", report.GatesPassed)
	out.WriteString("## Aggregate\n\n")
	out.WriteString("| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&out, "| MRR | %.6f |\n", report.Aggregate.MRR)
	for _, k := range report.TopK {
		fmt.Fprintf(&out, "| Hit@%d | %.6f |\n", k, report.Aggregate.HitAt[k])
		fmt.Fprintf(&out, "| nDCG@%d | %.6f |\n", k, report.Aggregate.NDCGAt[k])
	}
	fmt.Fprintf(&out, "| Invariant violations | %d |\n", report.Aggregate.InvariantViolations)
	if len(report.Cohorts) > 0 {
		out.WriteString("\n## Cohorts\n\n")
		out.WriteString("| Cohort | Queries | MRR | Invariant violations |\n")
		out.WriteString("| --- | ---: | ---: | ---: |\n")
		for _, cohort := range report.Cohorts {
			fmt.Fprintf(&out, "| %s | %d | %.6f | %d |\n",
				escapeMarkdown(string(cohort.Cohort)), cohort.QueryCount,
				cohort.MRR, cohort.InvariantViolations)
		}
	}
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
					escapeMarkdown(query.ID), escapeMarkdown(candidate.ID),
					escapeMarkdown(string(candidate.State)),
					escapeMarkdown(string(candidate.Decision)),
					escapeMarkdown(strings.Join(reasons, ", ")),
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
	if err := validateV3ReportRaw(data); err != nil {
		return Report{}, err
	}
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
	if (report.SchemaVersion != SchemaVersion &&
		report.SchemaVersion != LifecycleSchemaVersion &&
		report.SchemaVersion != CurrentReportSchemaVersion) ||
		strings.TrimSpace(report.DatasetVersion) == "" {
		return Report{}, fmt.Errorf("report schema_version and dataset_version are invalid")
	}
	if report.SchemaVersion == SchemaVersion && report.Lifecycle != nil {
		return Report{}, fmt.Errorf("report lifecycle section requires schema_version %d", LifecycleSchemaVersion)
	}
	if report.SchemaVersion == SchemaVersion {
		for _, query := range report.Queries {
			if query.Lifecycle != nil {
				return Report{}, fmt.Errorf("query lifecycle section requires schema_version %d", LifecycleSchemaVersion)
			}
		}
	}
	if report.SchemaVersion >= LifecycleSchemaVersion && report.Lifecycle == nil {
		return Report{}, fmt.Errorf("schema_version %d report requires lifecycle section", report.SchemaVersion)
	}
	if err := validateReportQueryContracts(report); err != nil {
		return Report{}, fmt.Errorf("decode report query contract: %w", err)
	}
	if report.SchemaVersion >= LifecycleSchemaVersion {
		if err := validateLifecycleReportPresence(report); err != nil {
			return Report{}, fmt.Errorf("decode report lifecycle fields: %w", err)
		}
		if err := validateLifecycleReport(report); err != nil {
			return Report{}, fmt.Errorf("decode report lifecycle: %w", err)
		}
	}
	if report.SchemaVersion == CurrentReportSchemaVersion {
		if err := validateV3Report(report); err != nil {
			return Report{}, fmt.Errorf("decode report v3: %w", err)
		}
	} else if report.Embedding.inputProfilePresent ||
		report.Configuration.present["retrieval_strategy"] ||
		report.Configuration.present["dense_candidate_limit"] ||
		report.Configuration.present["rrf_constant"] ||
		report.Cohorts != nil {
		return Report{}, fmt.Errorf("report v3 identity and cohort fields require schema_version %d", CurrentReportSchemaVersion)
	}
	return normalizeReport(report), nil
}

func validateReportQueryContracts(report Report) error {
	queryIDs := make(map[string]struct{}, len(report.Queries))
	for _, query := range report.Queries {
		if strings.TrimSpace(query.ID) == "" {
			return fmt.Errorf("query ID is required")
		}
		if _, duplicate := queryIDs[query.ID]; duplicate {
			return fmt.Errorf("duplicate query ID %q", query.ID)
		}
		queryIDs[query.ID] = struct{}{}
		if query.Target != "facts" && query.Target != "documents" {
			return fmt.Errorf("query %q field target is invalid", query.ID)
		}
		if query.Mode != "flat" && query.Mode != "hierarchical" {
			return fmt.Errorf("query %q field mode is invalid", query.ID)
		}
		if query.Target == "facts" && query.Mode != "flat" {
			return fmt.Errorf("query %q field mode must be flat for facts", query.ID)
		}
		switch report.SchemaVersion {
		case SchemaVersion:
			if query.Lifecycle != nil {
				return fmt.Errorf("query %q field lifecycle requires schema_version %d", query.ID, LifecycleSchemaVersion)
			}
		case LifecycleSchemaVersion, CurrentReportSchemaVersion:
			if query.Target == "facts" && query.Lifecycle == nil {
				return fmt.Errorf("query %q field lifecycle is required for facts", query.ID)
			}
			if query.Target == "documents" && query.Lifecycle != nil {
				return fmt.Errorf("query %q field lifecycle must be omitted for documents", query.ID)
			}
		}
		if report.SchemaVersion == CurrentReportSchemaVersion {
			if err := validateQueryCohorts(query.ID, query.Cohorts, query.Cohorts != nil); err != nil {
				return err
			}
		} else if query.Cohorts != nil {
			return fmt.Errorf("query %q field cohorts requires schema_version %d",
				query.ID, CurrentReportSchemaVersion)
		}
		if query.Lifecycle == nil {
			continue
		}
		intent := query.Lifecycle.Intent
		if !intent.valid() {
			return fmt.Errorf("query %q field intent is invalid", query.ID)
		}
		if intent == QueryIntentAsOf {
			if !validISODate(query.Lifecycle.AsOf) {
				return fmt.Errorf("query %q field as_of must use YYYY-MM-DD", query.ID)
			}
		} else if query.Lifecycle.AsOf != "" {
			return fmt.Errorf("query %q field as_of is only valid for as_of intent", query.ID)
		}
	}
	return nil
}

func validateV3Report(report Report) error {
	if report.Mode != "fixture" && report.Mode != "live" && report.Mode != "tei-fixture" {
		return fmt.Errorf("mode must be fixture, live, or tei-fixture")
	}
	if len(report.Queries) == 0 {
		return fmt.Errorf("report requires at least one query")
	}
	if len(report.Cohorts) == 0 {
		return fmt.Errorf("report requires at least one cohort aggregate")
	}
	if err := validateEmbeddingIdentity(report.Embedding, true); err != nil {
		return err
	}
	configuration := report.Configuration
	if err := validateBaseConfiguration(configuration); err != nil {
		return err
	}
	topK := append([]int(nil), configuration.TopK...)
	if err := normalizeTopK(&topK); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if !equalInts(topK, configuration.TopK) {
		return fmt.Errorf("configuration top_k must be sorted and unique")
	}
	if err := validateRetrievalConfiguration(configuration, true); err != nil {
		return err
	}
	reportTopK := append([]int(nil), report.TopK...)
	if err := normalizeTopK(&reportTopK); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if !equalInts(reportTopK, report.TopK) || !equalInts(report.TopK, configuration.TopK) {
		return fmt.Errorf("report top_k must be sorted, unique, and match configuration top_k")
	}
	if err := validateCohortAggregates(report); err != nil {
		return err
	}
	if err := validateDiagnostics(report.Diagnostics); err != nil {
		return err
	}
	queryMetrics := make([]QueryMetrics, len(report.Queries))
	for i := range report.Queries {
		if err := validateV3QueryMetrics(report.Queries[i].ID, report.Queries[i].Metrics, report.TopK); err != nil {
			return err
		}
		queryMetrics[i] = report.Queries[i].Metrics
	}
	expectedAggregate := Aggregate(queryMetrics, report.TopK)
	if report.Aggregate.InvariantViolations != expectedAggregate.InvariantViolations ||
		!metricClose(report.Aggregate.MRR, expectedAggregate.MRR) ||
		!metricMapClose(report.Aggregate.HitAt, expectedAggregate.HitAt, report.TopK) ||
		!metricMapClose(report.Aggregate.NDCGAt, expectedAggregate.NDCGAt, report.TopK) {
		return fmt.Errorf("aggregate metrics do not match query reports")
	}
	return nil
}

func validateDiagnostics(diagnostics *Diagnostics) error {
	if diagnostics == nil {
		return nil
	}
	validateSummary := func(name string, summary DurationSummary) error {
		if summary.Count < 0 || summary.Min < 0 || summary.P50 < summary.Min ||
			summary.P95 < summary.P50 || summary.Max < summary.P95 {
			return fmt.Errorf("diagnostics %s must be nonnegative and ordered", name)
		}
		if summary.Count == 0 && (summary.Min != 0 || summary.P50 != 0 || summary.P95 != 0 || summary.Max != 0) {
			return fmt.Errorf("diagnostics %s with zero count must have zero durations", name)
		}
		return nil
	}
	if err := validateSummary("query.total", diagnostics.Query.Total); err != nil {
		return err
	}
	if err := validateSummary("query.embed", diagnostics.Query.Embed); err != nil {
		return err
	}
	if err := validateSummary("query.search", diagnostics.Query.Search); err != nil {
		return err
	}
	if diagnostics.Corpus != nil &&
		(diagnostics.Corpus.EmbeddingDurationUS < 0 || diagnostics.Corpus.EmbeddingCount < 0) {
		return fmt.Errorf("diagnostics corpus values must be nonnegative")
	}
	return nil
}

func validateV3QueryMetrics(queryID string, metrics QueryMetrics, topK []int) error {
	if metrics.MRR < 0 || metrics.MRR > 1 ||
		!metricClose(metrics.MRR, metrics.MRR) {
		return fmt.Errorf("query %q MRR must be finite and between 0 and 1", queryID)
	}
	if len(metrics.HitAt) != len(topK) || len(metrics.NDCGAt) != len(topK) {
		return fmt.Errorf("query %q metric maps must exactly match top_k", queryID)
	}
	for _, k := range topK {
		hit, hitExists := metrics.HitAt[k]
		ndcg, ndcgExists := metrics.NDCGAt[k]
		if !hitExists || !ndcgExists ||
			hit < 0 || hit > 1 || ndcg < 0 || ndcg > 1 ||
			!metricClose(hit, hit) || !metricClose(ndcg, ndcg) {
			return fmt.Errorf("query %q metrics for k=%d must be finite and between 0 and 1", queryID, k)
		}
	}
	return nil
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateLifecycleReportPresence(report Report) error {
	if err := requireLifecycleFields(report.Lifecycle.present, "lifecycle", "aggregate", "transitions"); err != nil {
		return err
	}
	if report.Lifecycle.Transitions == nil {
		return fmt.Errorf("lifecycle.transitions must be an array")
	}
	aggregate := report.Lifecycle.Aggregate
	if err := requireLifecycleFields(
		aggregate.present,
		"lifecycle.aggregate",
		"checks",
		"violations",
		"canonical_preference_checks",
		"canonical_preference_violations",
	); err != nil {
		return err
	}
	for _, query := range report.Queries {
		if query.Lifecycle == nil {
			continue
		}
		lifecycleReport := query.Lifecycle
		if err := requireLifecycleFields(
			lifecycleReport.present,
			"query "+query.ID+".lifecycle",
			"intent", "candidates", "checks", "violations",
		); err != nil {
			return err
		}
		if lifecycleReport.Candidates == nil || lifecycleReport.Violations == nil {
			return fmt.Errorf("query %q lifecycle candidates and violations must be arrays", query.ID)
		}
		if lifecycleReport.Intent == QueryIntentAsOf {
			if !lifecycleReport.present["as_of"] {
				return fmt.Errorf("query %q lifecycle as_of is required", query.ID)
			}
		} else if lifecycleReport.present["as_of"] {
			return fmt.Errorf("query %q lifecycle as_of must be omitted", query.ID)
		}
		for _, candidate := range lifecycleReport.Candidates {
			if err := requireLifecycleFields(
				candidate.present,
				"query "+query.ID+" lifecycle candidate",
				"id", "canonical", "expired", "decision", "reason_codes", "valid",
			); err != nil {
				return err
			}
			if candidate.ReasonCodes == nil {
				return fmt.Errorf("query %q candidate %q reason_codes must be an array", query.ID, candidate.ID)
			}
			if candidate.Valid && !candidate.present["state"] {
				return fmt.Errorf("query %q valid candidate %q requires state", query.ID, candidate.ID)
			}
			if !candidate.Valid && candidate.present["state"] {
				return fmt.Errorf("query %q invalid candidate %q must omit state", query.ID, candidate.ID)
			}
		}
		for _, violation := range lifecycleReport.Violations {
			if err := validateLifecycleViolationPresence(violation); err != nil {
				return err
			}
		}
	}
	for _, transition := range report.Lifecycle.Transitions {
		if err := requireLifecycleFields(
			transition.present,
			"lifecycle transition",
			"id", "valid", "reason_code", "passed", "violations",
		); err != nil {
			return err
		}
		if transition.Violations == nil {
			return fmt.Errorf("transition %q violations must be an array", transition.ID)
		}
		for _, violation := range transition.Violations {
			if err := validateLifecycleViolationPresence(violation); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateLifecycleViolationPresence(violation LifecycleViolation) error {
	if err := requireLifecycleFields(
		violation.present, "lifecycle violation", "scope", "invariant",
	); err != nil {
		return err
	}
	switch violation.Scope {
	case ViolationScopeQuery:
		return requireLifecycleFields(
			violation.present, "query lifecycle violation", "query_id", "candidate_id",
		)
	case ViolationScopeTransition:
		return requireLifecycleFields(
			violation.present, "transition lifecycle violation", "scenario_id",
		)
	default:
		return nil
	}
}

func requireLifecycleFields(present map[string]bool, object string, fields ...string) error {
	for _, field := range fields {
		if !present[field] {
			return fmt.Errorf("%s field %q is required", object, field)
		}
	}
	return nil
}

func validateLifecycleReport(report Report) error {
	checks := 0
	violations := 0
	canonicalChecks := 0
	queryIDs := make(map[string]struct{}, len(report.Queries))
	for _, query := range report.Queries {
		if !safeReportIdentifier(query.ID) {
			return fmt.Errorf("query has invalid ID")
		}
		if _, duplicate := queryIDs[query.ID]; duplicate {
			return fmt.Errorf("duplicate query ID %q", query.ID)
		}
		queryIDs[query.ID] = struct{}{}
		if query.Target != "facts" && query.Target != "documents" {
			return fmt.Errorf("query %q has invalid target", query.ID)
		}
		if query.Target == "facts" && query.Lifecycle == nil {
			return fmt.Errorf("fact query %q requires lifecycle subsection", query.ID)
		}
		if query.Lifecycle == nil {
			continue
		}
		if query.Target == "documents" {
			return fmt.Errorf("document query %q must not contain lifecycle subsection", query.ID)
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
		if lifecycleReport.Checks == 0 && len(lifecycleReport.Violations) != 0 {
			return fmt.Errorf("query %q has violations without checks", query.ID)
		}
		checks += lifecycleReport.Checks
		violations += len(lifecycleReport.Violations)
		seen := make(map[string]struct{}, len(lifecycleReport.Candidates))
		hasCanonicalCurrent := false
		for _, candidate := range lifecycleReport.Candidates {
			if err := validateNormalizedPointID(candidate.ID); err != nil {
				return fmt.Errorf("query %q candidate ID is invalid", query.ID)
			}
			if _, duplicate := seen[candidate.ID]; duplicate {
				return fmt.Errorf("query %q has duplicate candidate ID %q", query.ID, candidate.ID)
			}
			seen[candidate.ID] = struct{}{}
			if candidate.Valid && !candidate.Expired &&
				candidate.State == lifecycle.Current && candidate.Canonical {
				hasCanonicalCurrent = true
			}
		}
		for _, candidate := range lifecycleReport.Candidates {
			if err := validateLifecycleCandidateTuple(
				query.ID, lifecycleReport.Intent, candidate, hasCanonicalCurrent,
			); err != nil {
				return err
			}
			if lifecycleReport.Intent == QueryIntentCurrent &&
				candidate.Valid && !candidate.Expired &&
				candidate.State == lifecycle.Current && !candidate.Canonical &&
				hasCanonicalCurrent {
				canonicalChecks++
			}
		}
		seenViolations := make(map[string]struct{}, len(lifecycleReport.Violations))
		for _, violation := range lifecycleReport.Violations {
			if err := validateQueryLifecycleViolation(violation, query.ID); err != nil {
				return err
			}
			_, candidateExists := seen[violation.CandidateID]
			if (violation.Invariant == InvariantCandidatePresent) == candidateExists {
				return fmt.Errorf("query %q lifecycle violation does not match candidate evidence", query.ID)
			}
			if _, duplicate := seenViolations[violation.key()]; duplicate {
				return fmt.Errorf("query %q has duplicate lifecycle violation", query.ID)
			}
			seenViolations[violation.key()] = struct{}{}
		}
	}
	transitionIDs := make(map[string]struct{}, len(report.Lifecycle.Transitions))
	for _, transition := range report.Lifecycle.Transitions {
		checks++
		violations += len(transition.Violations)
		if !safeReportIdentifier(transition.ID) {
			return fmt.Errorf("transition has invalid scenario ID")
		}
		if _, duplicate := transitionIDs[transition.ID]; duplicate {
			return fmt.Errorf("duplicate transition scenario ID %q", transition.ID)
		}
		transitionIDs[transition.ID] = struct{}{}
		if !transition.ReasonCode.validTransition() {
			return fmt.Errorf("transition %q has unknown reason code", transition.ID)
		}
		if transition.Valid != (transition.ReasonCode == ReasonTransitionValid) {
			return fmt.Errorf("transition %q has inconsistent validity and reason code", transition.ID)
		}
		if transition.Passed != (len(transition.Violations) == 0) {
			return fmt.Errorf("transition %q has inconsistent pass result", transition.ID)
		}
		seenViolations := make(map[string]struct{}, len(transition.Violations))
		for _, violation := range transition.Violations {
			if err := validateTransitionLifecycleViolation(violation, transition.ID); err != nil {
				return err
			}
			if _, duplicate := seenViolations[violation.key()]; duplicate {
				return fmt.Errorf("transition %q has duplicate lifecycle violation", transition.ID)
			}
			seenViolations[violation.key()] = struct{}{}
		}
	}
	aggregate := report.Lifecycle.Aggregate
	if aggregate.Checks != checks || aggregate.Violations != violations {
		return fmt.Errorf("aggregate check or violation count is inconsistent")
	}
	if aggregate.CanonicalPreferenceChecks != canonicalChecks ||
		aggregate.CanonicalPreferenceViolations != 0 {
		return fmt.Errorf("canonical preference counts are inconsistent")
	}
	return nil
}

func validateLifecycleCandidateTuple(
	queryID string,
	intent QueryIntent,
	candidate LifecycleCandidateReport,
	hasCanonicalCurrent bool,
) error {
	if !candidate.Decision.valid() {
		return fmt.Errorf("query %q candidate %q has invalid decision", queryID, candidate.ID)
	}
	if len(candidate.ReasonCodes) == 0 {
		return fmt.Errorf("query %q candidate %q has no reason code", queryID, candidate.ID)
	}
	for _, reason := range candidate.ReasonCodes {
		if !reason.validPresentation() {
			return fmt.Errorf("query %q candidate %q has unknown reason code", queryID, candidate.ID)
		}
	}
	var view lifecycle.View
	if candidate.Valid {
		view = lifecycle.View{
			State: candidate.State, Canonical: candidate.Canonical, Valid: true,
			Supersedes: []string{}, SupersededBy: []string{},
		}
		if candidate.State == lifecycle.Superseded {
			otherID := "0"
			if candidate.ID == otherID {
				otherID = "1"
			}
			view.SupersededBy = []string{otherID}
		}
		if err := lifecycle.Validate(view); err != nil {
			return fmt.Errorf("query %q candidate %q violates lifecycle constraints", queryID, candidate.ID)
		}
	} else {
		if candidate.State != "" {
			return fmt.Errorf("query %q invalid candidate %q exposes a state", queryID, candidate.ID)
		}
		view = lifecycle.View{Canonical: candidate.Canonical, Valid: false}
	}
	expected := decidePresentation(intent, view, candidate.Expired, hasCanonicalCurrent)
	if candidate.Decision != expected.Decision ||
		!equalReasonCodes(candidate.ReasonCodes, expected.ReasonCodes) {
		return fmt.Errorf("query %q candidate %q has inconsistent decision or reason codes", queryID, candidate.ID)
	}
	return nil
}

func validateQueryLifecycleViolation(violation LifecycleViolation, queryID string) error {
	if violation.Scope != ViolationScopeQuery ||
		violation.QueryID != queryID ||
		violation.ScenarioID != "" ||
		validateNormalizedPointID(violation.CandidateID) != nil {
		return fmt.Errorf("query %q contains invalid lifecycle violation identifiers", queryID)
	}
	switch violation.Invariant {
	case InvariantCandidatePresent, InvariantState, InvariantDecision, InvariantReasonCodes:
		return nil
	default:
		return fmt.Errorf("query %q contains unknown lifecycle violation invariant", queryID)
	}
}

func validateTransitionLifecycleViolation(violation LifecycleViolation, scenarioID string) error {
	if violation.Scope != ViolationScopeTransition ||
		violation.ScenarioID != scenarioID ||
		violation.QueryID != "" ||
		violation.CandidateID != "" {
		return fmt.Errorf("transition %q contains invalid lifecycle violation identifiers", scenarioID)
	}
	switch violation.Invariant {
	case InvariantTransitionValid, InvariantTransitionReason:
		return nil
	default:
		return fmt.Errorf("transition %q contains unknown lifecycle violation invariant", scenarioID)
	}
}

func safeReportIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}
