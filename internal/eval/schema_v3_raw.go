package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

func validateV3DatasetRaw(data []byte) error {
	return validateV3DatasetRawWithOptions(data, false)
}

func validateMaterializationDatasetRaw(data []byte) error {
	root, isV3 := rawVersionedRoot(data, CurrentDatasetSchemaVersion)
	if !isV3 {
		if root == nil {
			return fmt.Errorf("materialization input must be a JSON object with schema_version %d",
				CurrentDatasetSchemaVersion)
		}
		return fmt.Errorf("materialization requires schema_version %d", CurrentDatasetSchemaVersion)
	}
	return validateV3DatasetRawWithOptions(data, true)
}

func validateV3DatasetRawWithOptions(data []byte, allowMissingCorpusVectors bool) error {
	root, isV3 := rawVersionedRoot(data, CurrentDatasetSchemaVersion)
	if !isV3 {
		return nil
	}
	if err := requireRawFields(root, "dataset",
		"schema_version", "dataset_version", "embedding", "configuration",
		"facts", "chunks", "folders", "queries", "gates",
	); err != nil {
		return err
	}
	embedding, err := rawObjectField(root, "embedding", "dataset")
	if err != nil {
		return err
	}
	if err := requireRawFields(embedding, "embedding",
		"provider", "model_id", "model_revision", "dtype", "pooling",
		"vector_size", "input_profile",
	); err != nil {
		return err
	}
	configuration, err := rawObjectField(root, "configuration", "dataset")
	if err != nil {
		return err
	}
	if err := requireRawFields(configuration, "configuration",
		"name", "fact_collection", "chunk_collection", "folder_collection",
		"folder_top_k", "folder_threshold", "top_k", "retrieval_strategy",
		"dense_candidate_limit", "rrf_constant",
	); err != nil {
		return err
	}
	gates, err := rawObjectField(root, "gates", "dataset")
	if err != nil {
		return err
	}
	if err := requireRawFields(gates, "gates",
		"forbid_invariant_violations", "forbid_lifecycle_violations",
	); err != nil {
		return err
	}
	if err := rejectRawNulls(gates, "gates",
		"minimum_hit_at", "minimum_mrr", "minimum_ndcg_at",
	); err != nil {
		return err
	}
	for _, field := range []string{"minimum_hit_at", "minimum_ndcg_at"} {
		if raw, exists := gates[field]; exists {
			if err := validateRawFiniteNumberMap(raw, "gates."+field); err != nil {
				return err
			}
		}
	}
	if raw, exists := gates["minimum_mrr"]; exists {
		if err := validateRawFiniteNumber(raw, "gates.minimum_mrr"); err != nil {
			return err
		}
	}
	for _, field := range []string{"facts", "chunks", "folders"} {
		points, err := rawArrayField(root, field, "dataset")
		if err != nil {
			return err
		}
		for i, pointRaw := range points {
			point, err := rawObject(pointRaw, fmt.Sprintf("%s[%d]", field, i))
			if err != nil {
				return err
			}
			required := []string{"id", "payload"}
			if !allowMissingCorpusVectors {
				required = append(required, "vector")
			}
			if err := requireRawFields(point, fmt.Sprintf("%s[%d]", field, i), required...); err != nil {
				return err
			}
			if err := rejectRawNulls(point, fmt.Sprintf("%s[%d]", field, i), "vector"); err != nil {
				return err
			}
		}
	}
	queries, err := rawArrayField(root, "queries", "dataset")
	if err != nil {
		return err
	}
	for i, queryRaw := range queries {
		label := fmt.Sprintf("queries[%d]", i)
		query, err := rawObject(queryRaw, label)
		if err != nil {
			return err
		}
		if err := requireRawFields(query, label,
			"id", "target", "mode", "text", "expected", "cohorts",
		); err != nil {
			return err
		}
		if allowMissingCorpusVectors {
			if err := requireRawFields(query, label, "vector"); err != nil {
				return err
			}
		}
		if err := rejectRawNulls(query, label,
			"vector", "forbidden_ids", "intent", "as_of", "lifecycle_expectations",
		); err != nil {
			return err
		}
		expected, err := rawArrayField(query, "expected", label)
		if err != nil {
			return err
		}
		for j, expectedRaw := range expected {
			expectedItem, err := rawObject(expectedRaw, fmt.Sprintf("%s.expected[%d]", label, j))
			if err != nil {
				return err
			}
			if err := requireRawFields(expectedItem,
				fmt.Sprintf("%s.expected[%d]", label, j), "id", "grade"); err != nil {
				return err
			}
		}
		if raw, exists := query["lifecycle_expectations"]; exists {
			expectations, err := rawArray(raw, label+".lifecycle_expectations")
			if err != nil {
				return err
			}
			for j, expectationRaw := range expectations {
				expectation, err := rawObject(
					expectationRaw,
					fmt.Sprintf("%s.lifecycle_expectations[%d]", label, j),
				)
				if err != nil {
					return err
				}
				if err := requireRawFields(expectation,
					fmt.Sprintf("%s.lifecycle_expectations[%d]", label, j),
					"id", "decision",
				); err != nil {
					return err
				}
				if err := rejectRawNulls(expectation,
					fmt.Sprintf("%s.lifecycle_expectations[%d]", label, j),
					"state", "reason_codes",
				); err != nil {
					return err
				}
			}
		}
	}
	if raw, exists := root["transition_scenarios"]; exists {
		scenarios, err := rawArray(raw, "dataset.transition_scenarios")
		if err != nil {
			return err
		}
		for i, scenarioRaw := range scenarios {
			label := fmt.Sprintf("transition_scenarios[%d]", i)
			scenario, err := rawObject(scenarioRaw, label)
			if err != nil {
				return err
			}
			if err := requireRawFields(scenario, label,
				"id", "point_id", "source_lifecycle", "target_lifecycle", "expected_valid",
			); err != nil {
				return err
			}
			if err := rejectRawNulls(scenario, label, "expected_reason_code"); err != nil {
				return err
			}
			for _, field := range []string{"source_lifecycle", "target_lifecycle"} {
				payload, err := rawObjectField(scenario, field, label)
				if err != nil {
					return err
				}
				if err := validateRawLifecyclePayload(payload, label+"."+field); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateRawLifecyclePayload(payload map[string]json.RawMessage, label string) error {
	if err := requireRawFields(payload, label,
		"state", "canonical", "supersedes", "superseded_by",
	); err != nil {
		return err
	}
	if err := rejectRawNulls(payload, label, "provenance", "verified_at"); err != nil {
		return err
	}
	if raw, exists := payload["provenance"]; exists {
		provenance, err := rawObject(raw, label+".provenance")
		if err != nil {
			return err
		}
		if err := requireRawFields(provenance, label+".provenance", "source"); err != nil {
			return err
		}
		if err := rejectRawNulls(provenance, label+".provenance", "reference"); err != nil {
			return err
		}
	}
	return nil
}

func validateV3ReportRaw(data []byte) error {
	root, isV3 := rawVersionedRoot(data, CurrentReportSchemaVersion)
	if !isV3 {
		return nil
	}
	if err := requireRawFields(root, "report",
		"schema_version", "dataset_version", "mode", "embedding", "configuration",
		"top_k", "aggregate", "cohorts", "queries", "lifecycle", "gates_passed",
	); err != nil {
		return err
	}
	if err := rejectRawNulls(root, "report", "gate_failures"); err != nil {
		return err
	}
	if raw, exists := root["diagnostics"]; exists {
		diagnostics, err := rawObject(raw, "report.diagnostics")
		if err != nil {
			return err
		}
		if err := requireRawFields(diagnostics, "report.diagnostics", "query"); err != nil {
			return err
		}
		query, err := rawObjectField(diagnostics, "query", "report.diagnostics")
		if err != nil {
			return err
		}
		for _, field := range []string{"total", "embed", "search"} {
			summary, err := rawObjectField(query, field, "report.diagnostics.query")
			if err != nil {
				return err
			}
			if err := requireRawFields(summary, "report.diagnostics.query."+field,
				"count", "min_us", "p50_us", "p95_us", "max_us"); err != nil {
				return err
			}
			for _, value := range summary {
				if err := validateRawFiniteNumber(value, "report.diagnostics.query."+field); err != nil {
					return err
				}
			}
		}
		if corpusRaw, exists := diagnostics["corpus"]; exists {
			corpus, err := rawObject(corpusRaw, "report.diagnostics.corpus")
			if err != nil {
				return err
			}
			if err := requireRawFields(corpus, "report.diagnostics.corpus",
				"embedding_duration_us", "embedding_count"); err != nil {
				return err
			}
		}
	}
	embedding, err := rawObjectField(root, "embedding", "report")
	if err != nil {
		return err
	}
	if err := requireRawFields(embedding, "embedding",
		"provider", "model_id", "model_revision", "dtype", "pooling",
		"vector_size", "input_profile",
	); err != nil {
		return err
	}
	configuration, err := rawObjectField(root, "configuration", "report")
	if err != nil {
		return err
	}
	if err := requireRawFields(configuration, "configuration",
		"name", "fact_collection", "chunk_collection", "folder_collection",
		"folder_top_k", "folder_threshold", "top_k", "retrieval_strategy",
		"dense_candidate_limit", "rrf_constant",
	); err != nil {
		return err
	}
	aggregate, err := rawObjectField(root, "aggregate", "report")
	if err != nil {
		return err
	}
	if err := requireRawFields(aggregate, "aggregate",
		"hit_at", "mrr", "ndcg_at", "invariant_violations",
	); err != nil {
		return err
	}
	if err := validateRawMetricAggregate(aggregate, "aggregate"); err != nil {
		return err
	}
	cohorts, err := rawArrayField(root, "cohorts", "report")
	if err != nil {
		return err
	}
	for i, cohortRaw := range cohorts {
		label := fmt.Sprintf("cohorts[%d]", i)
		cohort, err := rawObject(cohortRaw, label)
		if err != nil {
			return err
		}
		if err := requireRawFields(cohort, label,
			"cohort", "query_count", "hit_at", "mrr", "ndcg_at", "invariant_violations",
		); err != nil {
			return err
		}
		if err := validateRawMetricAggregate(cohort, label); err != nil {
			return err
		}
		if err := validateRawFiniteNumber(cohort["query_count"], label+".query_count"); err != nil {
			return err
		}
	}
	queries, err := rawArrayField(root, "queries", "report")
	if err != nil {
		return err
	}
	for i, queryRaw := range queries {
		label := fmt.Sprintf("queries[%d]", i)
		query, err := rawObject(queryRaw, label)
		if err != nil {
			return err
		}
		if err := requireRawFields(query, label,
			"id", "target", "mode", "cohorts", "results", "metrics",
		); err != nil {
			return err
		}
		if err := rejectRawNulls(query, label, "lifecycle"); err != nil {
			return err
		}
		results, err := rawArrayField(query, "results", label)
		if err != nil {
			return err
		}
		for j, resultRaw := range results {
			resultLabel := fmt.Sprintf("%s.results[%d]", label, j)
			result, err := rawObject(resultRaw, resultLabel)
			if err != nil {
				return err
			}
			if err := requireRawFields(result, resultLabel, "id", "score"); err != nil {
				return err
			}
			if err := validateRawFiniteNumber(result["score"], resultLabel+".score"); err != nil {
				return err
			}
			if err := rejectRawNulls(result, resultLabel, "missing_text"); err != nil {
				return err
			}
		}
		metrics, err := rawObjectField(query, "metrics", label)
		if err != nil {
			return err
		}
		if err := requireRawFields(metrics, label+".metrics", "hit_at", "mrr", "ndcg_at"); err != nil {
			return err
		}
		for _, field := range []string{"hit_at", "ndcg_at"} {
			if err := validateRawFiniteNumberMap(
				metrics[field],
				label+".metrics."+field,
			); err != nil {
				return err
			}
		}
		if err := validateRawFiniteNumber(metrics["mrr"], label+".metrics.mrr"); err != nil {
			return err
		}
		if err := rejectRawNulls(metrics, label+".metrics",
			"invariant_violations", "missing_text_result_ids",
		); err != nil {
			return err
		}
	}
	return nil
}

func validateRawMetricAggregate(
	aggregate map[string]json.RawMessage,
	label string,
) error {
	for _, field := range []string{"hit_at", "ndcg_at"} {
		if err := validateRawFiniteNumberMap(aggregate[field], label+"."+field); err != nil {
			return err
		}
	}
	if err := validateRawFiniteNumber(aggregate["mrr"], label+".mrr"); err != nil {
		return err
	}
	return validateRawFiniteNumber(
		aggregate["invariant_violations"],
		label+".invariant_violations",
	)
}

func validateRawFiniteNumberMap(raw json.RawMessage, label string) error {
	values, err := rawObject(raw, label)
	if err != nil {
		return err
	}
	for _, valueRaw := range values {
		if err := validateRawFiniteNumber(valueRaw, label); err != nil {
			return err
		}
	}
	return nil
}

func validateRawFiniteNumber(raw json.RawMessage, label string) error {
	if rawIsNull(raw) {
		return fmt.Errorf("v3 %s must be a finite JSON number", label)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return fmt.Errorf("v3 %s must be a finite JSON number", label)
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("v3 %s must be a finite JSON number", label)
	}
	return nil
}

func rawVersionedRoot(data []byte, expectedVersion int) (map[string]json.RawMessage, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, false
	}
	raw, exists := root["schema_version"]
	if !exists || rawIsNull(raw) {
		return root, false
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return root, false
	}
	return root, version == expectedVersion
}

func requireRawFields(object map[string]json.RawMessage, label string, fields ...string) error {
	for _, field := range fields {
		raw, exists := object[field]
		if !exists {
			return fmt.Errorf("v3 %s field %q is required", label, field)
		}
		if rawIsNull(raw) {
			return fmt.Errorf("v3 %s field %q must not be null", label, field)
		}
	}
	return nil
}

func rejectRawNulls(object map[string]json.RawMessage, label string, fields ...string) error {
	for _, field := range fields {
		if raw, exists := object[field]; exists && rawIsNull(raw) {
			return fmt.Errorf("v3 %s field %q must not be null", label, field)
		}
	}
	return nil
}

func rawObjectField(
	object map[string]json.RawMessage,
	field string,
	label string,
) (map[string]json.RawMessage, error) {
	return rawObject(object[field], label+"."+field)
}

func rawArrayField(
	object map[string]json.RawMessage,
	field string,
	label string,
) ([]json.RawMessage, error) {
	return rawArray(object[field], label+"."+field)
}

func rawObject(raw json.RawMessage, label string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("v3 %s must be an object", label)
	}
	return object, nil
}

func rawArray(raw json.RawMessage, label string) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("v3 %s must be an array", label)
	}
	return values, nil
}

func rawIsNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
