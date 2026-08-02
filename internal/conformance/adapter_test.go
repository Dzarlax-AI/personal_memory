package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestClientProfilesCoverSupportedLiveFamilies(t *testing.T) {
	profiles := ClientProfiles()
	want := []ClientFamily{ClientCodex, ClientClaude, ClientChatGPT, ClientGenericMCP}
	if len(profiles) != len(want) {
		t.Fatalf("profiles = %#v", profiles)
	}
	for i := range want {
		if profiles[i].Family != want[i] {
			t.Fatalf("profile %d = %q, want %q", i, profiles[i].Family, want[i])
		}
	}
}

func TestCommandAdapterUsesStrictSafeProtocol(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewCommandAdapter(CommandAdapterOptions{
		ClientFamily: ClientCodex,
		Executable:   executable,
		Args:         []string{"-test.run=TestConformanceAdapterHelperProcess", "--"},
		Environment:  []string{"GO_WANT_CONFORMANCE_HELPER=success"},
	})
	if err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(strings.NewReader(validSuiteJSON))
	if err != nil {
		t.Fatal(err)
	}
	trace, err := adapter.Trace(context.Background(), suite.Scenarios[0], suite.ContractVersion)
	if err != nil {
		t.Fatal(err)
	}
	if trace.ClientFamily != ClientCodex || trace.ScenarioID != "TASK-002" {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestCommandAdapterFailsClosedWithoutLeakingOutput(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		limit int
		ctx   func() (context.Context, context.CancelFunc)
	}{
		{"malformed", "malformed", 1024, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}},
		{"oversized", "oversized", 8, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}},
		{"non-zero", "failure", 1024, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}},
		{"timeout", "sleep", 1024, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}},
	}
	suite, err := LoadSuite(strings.NewReader(validSuiteJSON))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewCommandAdapter(CommandAdapterOptions{
				ClientFamily: ClientCodex,
				Executable:   executable,
				Args:         []string{"-test.run=TestConformanceAdapterHelperProcess", "--"},
				Environment:  []string{"GO_WANT_CONFORMANCE_HELPER=" + tt.mode},
				OutputLimit:  tt.limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := tt.ctx()
			defer cancel()
			_, err = adapter.Trace(ctx, suite.Scenarios[0], suite.ContractVersion)
			if err == nil {
				t.Fatal("Trace() succeeded, want failure")
			}
			if tt.name == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Trace() error = %v, want deadline classification", err)
			}
			var exitErr *exec.ExitError
			if tt.name == "non-zero" && !errors.As(err, &exitErr) {
				t.Fatalf("Trace() error = %v, want exit classification", err)
			}
			for _, forbidden := range []string{"private prompt", "secret stderr", "raw response"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaks adapter content: %v", err)
				}
			}
		})
	}
}

func TestNewFixtureAdapterRejectsNilBundle(t *testing.T) {
	if _, err := NewFixtureAdapter(nil, ClientCodex); err == nil {
		t.Fatal("NewFixtureAdapter() accepted nil bundle")
	}
}

func TestNewCommandAdapterRejectsShellStyleExecutable(t *testing.T) {
	_, err := NewCommandAdapter(CommandAdapterOptions{
		ClientFamily: ClientCodex, Executable: "adapter --unsafe",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("NewCommandAdapter() error = %v", err)
	}
}

func TestConformanceAdapterHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_CONFORMANCE_HELPER")
	if mode == "" {
		return
	}
	_, _ = io.ReadAll(os.Stdin)
	switch mode {
	case "success":
		fmt.Print(validTraceJSON)
	case "malformed":
		fmt.Print(`{"raw response":"private prompt"}`)
	case "oversized":
		fmt.Print(strings.Repeat("x", 1024))
	case "failure":
		_, _ = fmt.Fprint(os.Stderr, "secret stderr")
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
	}
	os.Exit(0)
}
