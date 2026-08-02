package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Aggregate struct {
	Pass         int `json:"pass"`
	Fail         int `json:"fail"`
	Inconclusive int `json:"inconclusive"`
	Error        int `json:"error"`
}

type ScenarioResult struct {
	ScenarioID   string       `json:"scenario_id"`
	ClientFamily ClientFamily `json:"client_family"`
	Status       ResultStatus `json:"status"`
	Reasons      []ReasonCode `json:"reasons"`
}

type Report struct {
	SchemaVersion   int              `json:"schema_version"`
	ContractVersion string           `json:"contract_version"`
	SuiteVersion    string           `json:"suite_version"`
	Source          string           `json:"source"`
	GatesPassed     bool             `json:"gates_passed"`
	Aggregate       Aggregate        `json:"aggregate"`
	Results         []ScenarioResult `json:"results"`
}

func RenderJSON(report Report) ([]byte, error) {
	report = normalizeReport(report)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode conformance report: %w", err)
	}
	return append(data, '\n'), nil
}

func RenderMarkdown(report Report) string {
	report = normalizeReport(report)
	var out strings.Builder
	out.WriteString("# Model Memory Conformance Report\n\n")
	fmt.Fprintf(&out, "- Contract: `%s`\n", report.ContractVersion)
	fmt.Fprintf(&out, "- Suite: `%s`\n", report.SuiteVersion)
	fmt.Fprintf(&out, "- Source: `%s`\n", report.Source)
	fmt.Fprintf(&out, "- Gates passed: `%t`\n\n", report.GatesPassed)
	out.WriteString("| Pass | Fail | Inconclusive | Error |\n")
	out.WriteString("| ---: | ---: | ---: | ---: |\n")
	fmt.Fprintf(&out, "| %d | %d | %d | %d |\n\n",
		report.Aggregate.Pass, report.Aggregate.Fail,
		report.Aggregate.Inconclusive, report.Aggregate.Error)
	out.WriteString("| Client | Scenario | Status | Reasons |\n")
	out.WriteString("| --- | --- | --- | --- |\n")
	for _, result := range report.Results {
		reasons := make([]string, len(result.Reasons))
		for i, reason := range result.Reasons {
			reasons[i] = string(reason)
		}
		fmt.Fprintf(&out, "| %s | %s | %s | %s |\n",
			result.ClientFamily, result.ScenarioID, result.Status, strings.Join(reasons, ", "))
	}
	return out.String()
}

func DecodeReport(data []byte) (Report, error) {
	var report Report
	if err := decodeStrict(bytes.NewReader(data), &report); err != nil {
		return Report{}, fmt.Errorf("decode conformance report: %w", err)
	}
	if report.SchemaVersion != CurrentSchemaVersion {
		return Report{}, fmt.Errorf("report schema_version must be %d", CurrentSchemaVersion)
	}
	if !semver.MatchString(report.ContractVersion) || !semver.MatchString(report.SuiteVersion) {
		return Report{}, fmt.Errorf("report versions must be semantic versions")
	}
	if report.Source != "fixture" && report.Source != "live" {
		return Report{}, fmt.Errorf("report source is invalid")
	}
	if report.Results == nil {
		return Report{}, fmt.Errorf("report results must be an array")
	}
	expected := Aggregate{}
	seen := map[string]struct{}{}
	for _, result := range report.Results {
		if !safeScenarioID.MatchString(result.ScenarioID) || !validClientFamily(result.ClientFamily) {
			return Report{}, fmt.Errorf("report result identifiers are invalid")
		}
		if !validStatus(result.Status) {
			return Report{}, fmt.Errorf("report result status is invalid")
		}
		if result.Reasons == nil {
			return Report{}, fmt.Errorf("report result reasons must be an array")
		}
		if result.Status == StatusPass && len(result.Reasons) != 0 {
			return Report{}, fmt.Errorf("passing report result must not contain reasons")
		}
		if result.Status != StatusPass && len(result.Reasons) == 0 {
			return Report{}, fmt.Errorf("non-passing report result must contain reasons")
		}
		var previous ReasonCode
		for i, reason := range result.Reasons {
			if !validReason(reason) {
				return Report{}, fmt.Errorf("report result reason is invalid")
			}
			if i != 0 && reason <= previous {
				return Report{}, fmt.Errorf("report result reasons must be sorted and unique")
			}
			previous = reason
		}
		key := string(result.ClientFamily) + "\x00" + result.ScenarioID
		if _, duplicate := seen[key]; duplicate {
			return Report{}, fmt.Errorf("report contains duplicate result")
		}
		seen[key] = struct{}{}
		incrementAggregate(&expected, result.Status)
	}
	if report.Aggregate != expected {
		return Report{}, fmt.Errorf("report aggregate does not match results")
	}
	if report.GatesPassed != (expected.Fail == 0 && expected.Inconclusive == 0 && expected.Error == 0) {
		return Report{}, fmt.Errorf("report gates_passed does not match results")
	}
	return normalizeReport(report), nil
}

func normalizeReport(report Report) Report {
	report.Results = append([]ScenarioResult{}, report.Results...)
	for i := range report.Results {
		report.Results[i].Reasons = append([]ReasonCode{}, report.Results[i].Reasons...)
		sort.Slice(report.Results[i].Reasons, func(a, b int) bool {
			return report.Results[i].Reasons[a] < report.Results[i].Reasons[b]
		})
	}
	sort.Slice(report.Results, func(i, j int) bool {
		if report.Results[i].ClientFamily != report.Results[j].ClientFamily {
			return report.Results[i].ClientFamily < report.Results[j].ClientFamily
		}
		return report.Results[i].ScenarioID < report.Results[j].ScenarioID
	})
	return report
}

func validStatus(status ResultStatus) bool {
	switch status {
	case StatusPass, StatusFail, StatusInconclusive, StatusError:
		return true
	default:
		return false
	}
}

func validReason(reason ReasonCode) bool {
	switch reason {
	case ReasonRequiredEventMissing, ReasonForbiddenEvent, ReasonEventCount,
		ReasonEventOrder, ReasonRetryLimit, ReasonObservation, ReasonContractVersion,
		ReasonScenarioUnknown, ReasonAdapter:
		return true
	default:
		return false
	}
}
