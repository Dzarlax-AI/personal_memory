package todoist

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetTasksFilteredEncodesFilterExactly(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		io.WriteString(w, `{"results":[],"next_cursor":null}`)
	}))
	defer server.Close()

	client := NewClientWithHTTPClient("token", server.URL, server.Client())
	data, err := client.GetTasksFiltered(context.Background(), "#Work & today", 20)
	if err != nil {
		t.Fatalf("GetTasksFiltered: %v", err)
	}
	if gotQuery != "#Work & today" {
		t.Fatalf("query = %q, want exact filter", gotQuery)
	}
	if !strings.Contains(string(data), `"results"`) {
		t.Fatalf("paginated v1 response was not returned intact: %s", data)
	}
}

func TestGetTasksEncodesProjectIDExactly(t *testing.T) {
	var gotProjectID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProjectID = r.URL.Query().Get("project_id")
		io.WriteString(w, `{"results":[]}`)
	}))
	defer server.Close()

	client := NewClientWithHTTPClient("token", server.URL, server.Client())
	if _, err := client.GetTasks(context.Background(), "project / A&B", 7); err != nil {
		t.Fatalf("GetTasks: %v", err)
	}
	if gotProjectID != "project / A&B" {
		t.Fatalf("project_id = %q, want exact value", gotProjectID)
	}
}

func TestTaskIDsArePathEscaped(t *testing.T) {
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.RequestURI
		io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := NewClientWithHTTPClient("token", server.URL, server.Client())
	if _, err := client.UpdateTask(context.Background(), "task/with space", map[string]interface{}{"content": "x"}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if requestURI != "/tasks/task%2Fwith%20space" {
		t.Fatalf("RequestURI = %q, want escaped task id", requestURI)
	}
}

func TestNewClientWithHTTPClientDefaultsBlankBaseURL(t *testing.T) {
	client := NewClientWithHTTPClient("token", " \t ", nil)
	if client.baseURL != defaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, defaultBaseURL)
	}
	if client.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
}
