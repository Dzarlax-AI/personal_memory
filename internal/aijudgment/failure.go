package aijudgment

import (
	"context"
	"errors"
	"fmt"
)

type providerHTTPError struct{ status int }

func (e providerHTTPError) Error() string {
	return fmt.Sprintf("judgment provider returned HTTP %d", e.status)
}

type invalidResponseError struct{ err error }

func (e invalidResponseError) Error() string { return e.err.Error() }
func (e invalidResponseError) Unwrap() error { return e.err }

// FailureReason emits fixed codes, never a provider error message or payload.
func FailureReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.status {
		case 401, 403:
			return "provider_auth"
		case 429:
			return "provider_rate_limit"
		}
		return "provider_http"
	}
	var invalid invalidResponseError
	if errors.As(err, &invalid) {
		return "invalid_response"
	}
	return "provider_transport"
}
