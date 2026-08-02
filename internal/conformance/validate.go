package conformance

import "sort"

type ResultStatus string

const (
	StatusPass         ResultStatus = "pass"
	StatusFail         ResultStatus = "fail"
	StatusInconclusive ResultStatus = "inconclusive"
	StatusError        ResultStatus = "error"
)

type ReasonCode string

const (
	ReasonRequiredEventMissing ReasonCode = "required_event_missing"
	ReasonForbiddenEvent       ReasonCode = "forbidden_event_present"
	ReasonEventCount           ReasonCode = "event_count_invalid"
	ReasonEventOrder           ReasonCode = "event_order_invalid"
	ReasonRetryLimit           ReasonCode = "retry_limit_exceeded"
	ReasonObservation          ReasonCode = "observation_incomplete"
	ReasonContractVersion      ReasonCode = "contract_version_mismatch"
	ReasonScenarioUnknown      ReasonCode = "scenario_unknown"
	ReasonAdapter              ReasonCode = "adapter_error"
)

type ValidationResult struct {
	Status  ResultStatus
	Reasons []ReasonCode
}

func ValidateScenario(scenario Scenario, trace Trace, contractVersion string) ValidationResult {
	if trace.ContractVersion != contractVersion {
		return validationResult(StatusError, ReasonContractVersion)
	}
	if trace.ScenarioID != scenario.ID {
		return validationResult(StatusError, ReasonScenarioUnknown)
	}
	observed := make(map[Observation]struct{}, len(trace.Observed))
	for _, observation := range trace.Observed {
		observed[observation] = struct{}{}
	}
	for _, required := range scenario.RequiredObservations {
		if _, exists := observed[required]; !exists {
			return validationResult(StatusInconclusive, ReasonObservation)
		}
	}

	reasons := make([]ReasonCode, 0)
	for _, pattern := range scenario.Assertions.Must {
		if countMatches(trace.Events, pattern) == 0 {
			reasons = append(reasons, ReasonRequiredEventMissing)
		}
	}
	for _, pattern := range scenario.Assertions.MustNot {
		if countMatches(trace.Events, pattern) != 0 {
			reasons = append(reasons, ReasonForbiddenEvent)
		}
	}
	for _, assertion := range scenario.Assertions.Counts {
		count := countMatches(trace.Events, assertion.Pattern)
		if assertion.Min != nil && count < *assertion.Min ||
			assertion.Max != nil && count > *assertion.Max {
			reasons = append(reasons, ReasonEventCount)
		}
	}
	for _, assertion := range scenario.Assertions.Ordered {
		before, beforeFound := firstMatch(trace.Events, assertion.Before)
		after, afterFound := firstMatch(trace.Events, assertion.After)
		if !beforeFound || !afterFound || before.Sequence >= after.Sequence {
			reasons = append(reasons, ReasonEventOrder)
		}
	}
	if scenario.Assertions.MaxRetries != nil {
		retries := 0
		for _, event := range trace.Events {
			if event.RetryOf != nil {
				retries++
			}
		}
		if retries > *scenario.Assertions.MaxRetries {
			reasons = append(reasons, ReasonRetryLimit)
		}
	}
	if len(reasons) == 0 {
		return ValidationResult{Status: StatusPass, Reasons: []ReasonCode{}}
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	reasons = compactReasons(reasons)
	return ValidationResult{Status: StatusFail, Reasons: reasons}
}

func validationResult(status ResultStatus, reason ReasonCode) ValidationResult {
	return ValidationResult{Status: status, Reasons: []ReasonCode{reason}}
}

func countMatches(events []Event, pattern EventPattern) int {
	count := 0
	for _, event := range events {
		if eventMatches(event, pattern) {
			count++
		}
	}
	return count
}

func firstMatch(events []Event, pattern EventPattern) (Event, bool) {
	for _, event := range events {
		if eventMatches(event, pattern) {
			return event, true
		}
	}
	return Event{}, false
}

func eventMatches(event Event, pattern EventPattern) bool {
	return event.Event == pattern.Event &&
		(pattern.Capability == "" || event.Capability == pattern.Capability) &&
		(pattern.Operation == "" || event.Operation == pattern.Operation) &&
		(pattern.Outcome == "" || event.Outcome == pattern.Outcome) &&
		(pattern.Code == "" || event.Code == pattern.Code)
}

func compactReasons(reasons []ReasonCode) []ReasonCode {
	if len(reasons) < 2 {
		return reasons
	}
	output := reasons[:1]
	for _, reason := range reasons[1:] {
		if reason != output[len(output)-1] {
			output = append(output, reason)
		}
	}
	return output
}
