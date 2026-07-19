package todoist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func todoistRequest(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func rejectingTodoistServer(t *testing.T) (*Server, func()) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("validation failure reached Todoist backend: %s %s", r.Method, r.URL)
	}))
	client := NewClientWithHTTPClient("token", backend.URL, backend.Client())
	return NewServer(client), backend.Close
}

func TestCreateTaskRejectsBlankContent(t *testing.T) {
	s, closeServer := rejectingTodoistServer(t)
	defer closeServer()
	result, err := s.createTask(context.Background(), todoistRequest(map[string]interface{}{"content": " \t"}))
	if err != nil || !result.IsError {
		t.Fatalf("result=%#v err=%v, want tool error", result, err)
	}
}

func TestCreateAndUpdateSchemasExposeLabels(t *testing.T) {
	registry := mcpserver.NewMCPServer("todoist-test", "1.0.0")
	NewServer(nil).RegisterTools(registry)
	for _, toolName := range []string{"create_task", "update_task"} {
		tool := registry.GetTool(toolName)
		if tool == nil {
			t.Fatalf("tool %q was not registered", toolName)
		}
		labels, ok := tool.Tool.InputSchema.Properties["labels"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s labels schema missing or wrong type: %#v", toolName, tool.Tool.InputSchema.Properties["labels"])
		}
		if labels["type"] != "array" || labels["maxItems"] != maxLabels {
			t.Fatalf("%s labels schema = %#v", toolName, labels)
		}
	}
}

func TestGetTasksRejectsInvalidLimits(t *testing.T) {
	s, closeServer := rejectingTodoistServer(t)
	defer closeServer()
	for _, limit := range []float64{0, 101, 1.5} {
		result, err := s.getTasks(context.Background(), todoistRequest(map[string]interface{}{"limit": limit}))
		if err != nil || !result.IsError {
			t.Fatalf("limit=%v result=%#v err=%v, want tool error", limit, result, err)
		}
	}
}

func TestCreateTaskRejectsInvalidPriorityAndLabels(t *testing.T) {
	s, closeServer := rejectingTodoistServer(t)
	defer closeServer()
	tests := []map[string]interface{}{
		{"content": "valid", "priority": 2.5},
		{"content": "valid", "priority": float64(5)},
		{"content": "valid", "labels": []interface{}{"ok", ""}},
		{"content": "valid", "labels": []interface{}{strings.Repeat("x", maxLabelLength+1)}},
	}
	for _, args := range tests {
		result, err := s.createTask(context.Background(), todoistRequest(args))
		if err != nil || !result.IsError {
			t.Fatalf("args=%#v result=%#v err=%v, want tool error", args, result, err)
		}
	}
}

func TestUpdateTaskRejectsBlankIDAndEmptyUpdate(t *testing.T) {
	s, closeServer := rejectingTodoistServer(t)
	defer closeServer()
	for _, args := range []map[string]interface{}{{"task_id": " "}, {"task_id": "123"}} {
		result, err := s.updateTask(context.Background(), todoistRequest(args))
		if err != nil || !result.IsError {
			t.Fatalf("args=%#v result=%#v err=%v, want tool error", args, result, err)
		}
	}
}
