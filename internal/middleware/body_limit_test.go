package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestBodyLimitRejectsOversizedContentLength(t *testing.T) {
	called := false
	handler := RequestBodyLimit(4)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodPost, "/memory", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if called {
		t.Fatal("oversized request reached downstream handler")
	}
}

func TestRequestBodyLimitCapsChunkedBody(t *testing.T) {
	handler := RequestBodyLimit(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		var maxErr *http.MaxBytesError
		if !errors.As(err, &maxErr) {
			t.Fatalf("read error=%v, want *http.MaxBytesError", err)
		}
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
	}))
	req := httptest.NewRequest(http.MethodPost, "/memory", strings.NewReader("12345"))
	req.ContentLength = -1
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRequestBodyLimitAllowsBodyAtLimit(t *testing.T) {
	handler := RequestBodyLimit(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/memory", strings.NewReader("1234"))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "1234" {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
