package main

import "testing"

func TestValidateCLIOptions(t *testing.T) {
	base := cliOptions{qdrantURL: "http://qdrant:6333", collectionName: "memory"}
	tests := []struct {
		name      string
		mutate    func(*cliOptions)
		wantError bool
	}{
		{name: "dry run"},
		{name: "apply", mutate: func(o *cliOptions) {
			o.apply = true
			o.writesStopped = true
			o.manifestPath = "/tmp/rollback.jsonl"
		}},
		{name: "apply without stopped writers", mutate: func(o *cliOptions) {
			o.apply = true
			o.manifestPath = "/tmp/rollback.jsonl"
		}, wantError: true},
		{name: "apply without manifest", mutate: func(o *cliOptions) {
			o.apply = true
			o.writesStopped = true
		}, wantError: true},
		{name: "rollback", mutate: func(o *cliOptions) {
			o.rollbackPath = "/tmp/rollback.jsonl"
			o.writesStopped = true
		}},
		{name: "rollback without stopped writers", mutate: func(o *cliOptions) {
			o.rollbackPath = "/tmp/rollback.jsonl"
		}, wantError: true},
		{name: "conflicting apply rollback", mutate: func(o *cliOptions) {
			o.apply = true
			o.writesStopped = true
			o.rollbackPath = "/tmp/rollback.jsonl"
		}, wantError: true},
		{name: "dry run with manifest", mutate: func(o *cliOptions) {
			o.manifestPath = "/tmp/rollback.jsonl"
		}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			if test.mutate != nil {
				test.mutate(&options)
			}
			err := validateCLIOptions(options)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}
