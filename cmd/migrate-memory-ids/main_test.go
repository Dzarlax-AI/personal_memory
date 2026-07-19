package main

import "testing"

func TestValidateApplyPreconditions(t *testing.T) {
	tests := []struct {
		name          string
		apply         bool
		writesStopped bool
		wantError     bool
	}{
		{name: "dry run", apply: false, writesStopped: false},
		{name: "confirmed apply", apply: true, writesStopped: true},
		{name: "unconfirmed apply", apply: true, writesStopped: false, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateApplyPreconditions(test.apply, test.writesStopped)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}
