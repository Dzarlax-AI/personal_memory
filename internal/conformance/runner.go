package conformance

import (
	"context"
	"fmt"
	"sort"
)

func Run(suite *Suite, bundle *TraceBundle, catalog ContractCatalog, source string) (Report, error) {
	if source != "fixture" && source != "live" {
		return Report{}, fmt.Errorf("source must be fixture or live")
	}
	if err := validateSuite(suite); err != nil {
		return Report{}, err
	}
	if bundle.SchemaVersion != CurrentSchemaVersion {
		return Report{}, fmt.Errorf("trace bundle schema_version must be %d", CurrentSchemaVersion)
	}
	if err := ValidateCoverage(suite, catalog); err != nil {
		return Report{}, err
	}
	if bundle.ContractVersion != suite.ContractVersion {
		return Report{}, fmt.Errorf("trace bundle contract version does not match suite")
	}
	scenarios := make(map[string]Scenario, len(suite.Scenarios))
	for _, scenario := range suite.Scenarios {
		scenarios[scenario.ID] = scenario
	}
	clients := map[ClientFamily]struct{}{}
	traces := map[string]Trace{}
	for _, trace := range bundle.Traces {
		if err := validateTrace(&trace, bundle.ContractVersion); err != nil {
			return Report{}, err
		}
		if _, exists := scenarios[trace.ScenarioID]; !exists {
			return Report{}, fmt.Errorf("trace references unknown scenario %q", trace.ScenarioID)
		}
		clients[trace.ClientFamily] = struct{}{}
		traces[string(trace.ClientFamily)+"\x00"+trace.ScenarioID] = trace
	}
	if len(clients) == 0 {
		return Report{}, fmt.Errorf("trace bundle contains no client traces")
	}
	clientList := make([]ClientFamily, 0, len(clients))
	for client := range clients {
		clientList = append(clientList, client)
	}
	sort.Slice(clientList, func(i, j int) bool { return clientList[i] < clientList[j] })
	scenarioList := append([]Scenario{}, suite.Scenarios...)
	sort.Slice(scenarioList, func(i, j int) bool { return scenarioList[i].ID < scenarioList[j].ID })

	report := Report{
		SchemaVersion: CurrentSchemaVersion, ContractVersion: suite.ContractVersion,
		SuiteVersion: suite.SuiteVersion, Source: source, Results: []ScenarioResult{},
	}
	for _, client := range clientList {
		for _, scenario := range scenarioList {
			trace, exists := traces[string(client)+"\x00"+scenario.ID]
			result := ValidationResult{Status: StatusInconclusive, Reasons: []ReasonCode{ReasonObservation}}
			if exists {
				result = ValidateScenario(scenario, trace, suite.ContractVersion)
			}
			report.Results = append(report.Results, ScenarioResult{
				ScenarioID: scenario.ID, ClientFamily: client,
				Status: result.Status, Reasons: result.Reasons,
			})
			incrementAggregate(&report.Aggregate, result.Status)
		}
	}
	report.GatesPassed = report.Aggregate.Fail == 0 &&
		report.Aggregate.Inconclusive == 0 && report.Aggregate.Error == 0
	return normalizeReport(report), nil
}

func RunAdapter(
	ctx context.Context,
	suite *Suite,
	catalog ContractCatalog,
	adapter Adapter,
) (Report, error) {
	if err := validateSuite(suite); err != nil {
		return Report{}, err
	}
	if err := ValidateCoverage(suite, catalog); err != nil {
		return Report{}, err
	}
	if adapter == nil || !validLiveClientFamily(adapter.ClientFamily()) {
		return Report{}, fmt.Errorf("live adapter is invalid")
	}
	scenarios := append([]Scenario{}, suite.Scenarios...)
	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })
	report := Report{
		SchemaVersion: CurrentSchemaVersion, ContractVersion: suite.ContractVersion,
		SuiteVersion: suite.SuiteVersion, Source: "live", Results: []ScenarioResult{},
	}
	for _, scenario := range scenarios {
		result := ValidationResult{}
		trace, err := adapter.Trace(ctx, scenario, suite.ContractVersion)
		if err != nil {
			result = validationResult(StatusError, ReasonAdapter)
		} else if err := validateTrace(&trace, suite.ContractVersion); err != nil {
			result = validationResult(StatusError, ReasonAdapter)
		} else {
			result = ValidateScenario(scenario, trace, suite.ContractVersion)
		}
		report.Results = append(report.Results, ScenarioResult{
			ScenarioID: scenario.ID, ClientFamily: adapter.ClientFamily(),
			Status: result.Status, Reasons: result.Reasons,
		})
		incrementAggregate(&report.Aggregate, result.Status)
	}
	report.GatesPassed = report.Aggregate.Fail == 0 &&
		report.Aggregate.Inconclusive == 0 && report.Aggregate.Error == 0
	return normalizeReport(report), nil
}

func incrementAggregate(aggregate *Aggregate, status ResultStatus) {
	switch status {
	case StatusPass:
		aggregate.Pass++
	case StatusFail:
		aggregate.Fail++
	case StatusInconclusive:
		aggregate.Inconclusive++
	case StatusError:
		aggregate.Error++
	}
}
