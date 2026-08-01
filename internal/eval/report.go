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
	if report.SchemaVersion != SchemaVersion || strings.TrimSpace(report.DatasetVersion) == "" {
		return Report{}, fmt.Errorf("report schema_version and dataset_version are invalid")
	}
	return normalizeReport(report), nil
}
