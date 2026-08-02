package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
)

const defaultAdapterOutputLimit = 1 << 20

// Adapter produces normalized traces for one client family.
type Adapter interface {
	ClientFamily() ClientFamily
	Trace(context.Context, Scenario, string) (Trace, error)
}

// ClientProfile identifies a live client supported by the common adapter protocol.
type ClientProfile struct {
	Family ClientFamily
}

// ClientProfiles returns the supported live client families in stable order.
func ClientProfiles() []ClientProfile {
	return []ClientProfile{
		{Family: ClientCodex},
		{Family: ClientClaude},
		{Family: ClientChatGPT},
		{Family: ClientGenericMCP},
	}
}

// FixtureAdapter reads traces from a validated fixture bundle.
type FixtureAdapter struct {
	client ClientFamily
	traces map[string]Trace
}

// NewFixtureAdapter selects one client family's traces from a fixture bundle.
func NewFixtureAdapter(bundle *TraceBundle, client ClientFamily) (*FixtureAdapter, error) {
	if !validClientFamily(client) {
		return nil, fmt.Errorf("fixture adapter client family is invalid")
	}
	if bundle == nil {
		return nil, fmt.Errorf("fixture bundle must not be nil")
	}
	traces := make(map[string]Trace)
	for _, trace := range bundle.Traces {
		if trace.ClientFamily == client {
			traces[trace.ScenarioID] = trace
		}
	}
	if len(traces) == 0 {
		return nil, fmt.Errorf("fixture bundle contains no traces for client %q", client)
	}
	return &FixtureAdapter{client: client, traces: traces}, nil
}

// ClientFamily returns the adapter's normalized client family.
func (adapter *FixtureAdapter) ClientFamily() ClientFamily { return adapter.client }

// Trace returns the fixture trace for a scenario.
func (adapter *FixtureAdapter) Trace(_ context.Context, scenario Scenario, _ string) (Trace, error) {
	trace, exists := adapter.traces[scenario.ID]
	if !exists {
		return Trace{}, fmt.Errorf("fixture trace is unavailable")
	}
	return trace, nil
}

// CommandAdapter runs an external adapter through the normalized JSON protocol.
type CommandAdapter struct {
	client      ClientFamily
	executable  string
	args        []string
	environment []string
	outputLimit int
}

// CommandAdapterOptions configures a fail-closed external command adapter.
type CommandAdapterOptions struct {
	ClientFamily ClientFamily
	Executable   string
	Args         []string
	// Environment completely replaces the child process environment, including when empty.
	Environment []string
	OutputLimit int
}

// AdapterRequest is the privacy-safe scenario request sent to a live adapter.
type AdapterRequest struct {
	SchemaVersion   int                            `json:"schema_version"`
	ContractVersion string                         `json:"contract_version"`
	ClientFamily    ClientFamily                   `json:"client_family"`
	ScenarioID      string                         `json:"scenario_id"`
	IntentClass     string                         `json:"intent_class"`
	SyntheticInput  string                         `json:"synthetic_input"`
	Capabilities    map[Capability]CapabilityState `json:"capabilities"`
}

// NewCommandAdapter validates options and constructs an external command adapter.
func NewCommandAdapter(options CommandAdapterOptions) (*CommandAdapter, error) {
	if !validLiveClientFamily(options.ClientFamily) {
		return nil, fmt.Errorf("command adapter client family is invalid")
	}
	if !filepath.IsAbs(options.Executable) {
		return nil, fmt.Errorf("command adapter executable must be an absolute path")
	}
	limit := options.OutputLimit
	if limit == 0 {
		limit = defaultAdapterOutputLimit
	}
	if limit < 1 {
		return nil, fmt.Errorf("command adapter output limit must be positive")
	}
	return &CommandAdapter{
		client: options.ClientFamily, executable: options.Executable,
		args:        append([]string{}, options.Args...),
		environment: append([]string{}, options.Environment...), outputLimit: limit,
	}, nil
}

// ClientFamily returns the adapter's normalized client family.
func (adapter *CommandAdapter) ClientFamily() ClientFamily { return adapter.client }

// Trace runs the adapter once and strictly validates its normalized trace.
func (adapter *CommandAdapter) Trace(ctx context.Context, scenario Scenario, contract string) (Trace, error) {
	request := AdapterRequest{
		SchemaVersion: CurrentSchemaVersion, ContractVersion: contract,
		ClientFamily: adapter.client, ScenarioID: scenario.ID,
		IntentClass: scenario.IntentClass, SyntheticInput: scenario.SyntheticInput,
		Capabilities: cloneCapabilities(scenario.Capabilities),
	}
	input, err := json.Marshal(request)
	if err != nil {
		return Trace{}, fmt.Errorf("encode adapter request")
	}
	input = append(input, '\n')
	command := exec.CommandContext(ctx, adapter.executable, adapter.args...)
	command.Env = append([]string{}, adapter.environment...)
	command.Stdin = bytes.NewReader(input)
	output := &limitedBuffer{limit: adapter.outputLimit}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Trace{}, fmt.Errorf("adapter process context failed: %w", contextErr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return Trace{}, fmt.Errorf("adapter process exited unsuccessfully: %w", exitErr)
		}
		return Trace{}, fmt.Errorf("adapter process failed: %w", err)
	}
	trace, err := DecodeTrace(output.Bytes())
	if err != nil {
		return Trace{}, fmt.Errorf("adapter returned invalid trace")
	}
	if trace.ClientFamily != adapter.client || trace.ScenarioID != scenario.ID ||
		trace.ContractVersion != contract {
		return Trace{}, fmt.Errorf("adapter trace identity mismatch")
	}
	return trace, nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		return 0, fmt.Errorf("adapter output exceeds limit")
	}
	if len(data) > remaining {
		_, _ = buffer.buffer.Write(data[:remaining])
		return remaining, fmt.Errorf("adapter output exceeds limit")
	}
	return buffer.buffer.Write(data)
}

func (buffer *limitedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func cloneCapabilities(input map[Capability]CapabilityState) map[Capability]CapabilityState {
	output := make(map[Capability]CapabilityState, len(input))
	for capability, state := range input {
		output[capability] = state
	}
	return output
}

func validLiveClientFamily(client ClientFamily) bool {
	switch client {
	case ClientCodex, ClientClaude, ClientChatGPT, ClientGenericMCP:
		return true
	default:
		return false
	}
}
