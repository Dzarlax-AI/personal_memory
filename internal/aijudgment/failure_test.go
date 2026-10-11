package aijudgment

import (
	"context"
	"errors"
	"testing"
)

func TestFailureReasonSafeCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, ""}, {context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {providerHTTPError{401}, "provider_auth"}, {providerHTTPError{403}, "provider_auth"}, {providerHTTPError{429}, "provider_rate_limit"}, {providerHTTPError{503}, "provider_http"}, {invalidResponseError{errors.New("SECRET payload")}, "invalid_response"}, {errors.New("SECRET /private/url"), "provider_transport"}} {
		if got := FailureReason(tc.err); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
