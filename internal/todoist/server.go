package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	client *Client
}

const (
	maxLabels      = 100
	maxLabelLength = 255
)

func NewServer(client *Client) *Server {
	return &Server{client: client}
}

func (s *Server) RegisterTools(srv *server.MCPServer) {
	srv.AddTool(mcp.NewTool("get_projects",
		mcp.WithDescription("List all Todoist projects."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	), s.getProjects)

	srv.AddTool(mcp.NewTool("get_labels",
		mcp.WithDescription("List all Todoist labels."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	), s.getLabels)

	srv.AddTool(mcp.NewTool("get_tasks",
		mcp.WithDescription("Get tasks, optionally filtered by project or Todoist filter query."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithString("project_id", mcp.Description("Filter by project ID")),
		mcp.WithString("filter", mcp.Description("Todoist filter query (e.g. 'today', 'overdue', '#Work')")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 20)"), mcp.Min(1), mcp.Max(100)),
	), s.getTasks)

	srv.AddTool(mcp.NewTool("create_task",
		mcp.WithDescription("Create a new Todoist task."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithString("content", mcp.Description("Task title/description"), mcp.Required()),
		mcp.WithString("project_id", mcp.Description("Project to add task to")),
		mcp.WithString("due_string", mcp.Description("Natural language due date")),
		mcp.WithNumber("priority", mcp.Description("1 (normal) to 4 (urgent)"), mcp.Min(1), mcp.Max(4), mcp.MultipleOf(1)),
		mcp.WithArray("labels", mcp.Description("Todoist label names"), mcp.WithStringItems(mcp.MinLength(1), mcp.MaxLength(maxLabelLength)), mcp.MaxItems(maxLabels), mcp.UniqueItems(true)),
	), s.createTask)

	srv.AddTool(mcp.NewTool("update_task",
		mcp.WithDescription("Update an existing Todoist task."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithString("task_id", mcp.Description("Task ID"), mcp.Required()),
		mcp.WithString("content", mcp.Description("New title")),
		mcp.WithString("due_string", mcp.Description("New due date")),
		mcp.WithNumber("priority", mcp.Description("New priority (1-4)"), mcp.Min(1), mcp.Max(4), mcp.MultipleOf(1)),
		mcp.WithArray("labels", mcp.Description("Replacement Todoist label names"), mcp.WithStringItems(mcp.MinLength(1), mcp.MaxLength(maxLabelLength)), mcp.MaxItems(maxLabels), mcp.UniqueItems(true)),
	), s.updateTask)

	srv.AddTool(mcp.NewTool("delete_task",
		mcp.WithDescription("Delete a Todoist task."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithString("task_id", mcp.Description("Task ID"), mcp.Required()),
	), s.deleteTask)

	srv.AddTool(mcp.NewTool("complete_task",
		mcp.WithDescription("Complete a Todoist task."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithString("task_id", mcp.Description("Task ID"), mcp.Required()),
	), s.completeTask)
}

func (s *Server) getProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := s.client.GetProjects(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(formatJSON(data)), nil
}

func (s *Server) getLabels(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := s.client.GetLabels(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(formatJSON(data)), nil
}

func (s *Server) getTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	projectID := strParam(args, "project_id")
	filter := strParam(args, "filter")
	limit := intParam(args, "limit", 20)
	if limit < 1 || limit > 100 {
		return mcp.NewToolResultError("limit must be an integer between 1 and 100"), nil
	}

	var data []byte
	var err error
	if filter != "" {
		data, err = s.client.GetTasksFiltered(ctx, filter, limit)
	} else {
		data, err = s.client.GetTasks(ctx, projectID, limit)
	}
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(formatJSON(data)), nil
}

func (s *Server) createTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	content := strParam(args, "content")
	if strings.TrimSpace(content) == "" {
		return mcp.NewToolResultError("content is required and must not be blank"), nil
	}
	task := make(map[string]interface{})
	task["content"] = content
	if v := strParam(args, "project_id"); v != "" {
		task["project_id"] = v
	}
	if v := strParam(args, "due_string"); v != "" {
		task["due_string"] = v
	}
	if v, ok := args["priority"]; ok && v != nil {
		priority, err := validatePriority(v)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		task["priority"] = priority
	}
	if v, ok := args["labels"]; ok && v != nil {
		labels, err := validateLabels(v)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		task["labels"] = labels
	}

	data, err := s.client.CreateTask(ctx, task)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(formatJSON(data)), nil
}

func (s *Server) updateTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	taskID := strParam(args, "task_id")
	if strings.TrimSpace(taskID) == "" {
		return mcp.NewToolResultError("task_id is required and must not be blank"), nil
	}

	update := make(map[string]interface{})
	if v, ok := args["content"]; ok && v != nil {
		content, ok := v.(string)
		if !ok || strings.TrimSpace(content) == "" {
			return mcp.NewToolResultError("content must not be blank"), nil
		}
		update["content"] = content
	}
	if v := strParam(args, "due_string"); v != "" {
		update["due_string"] = v
	}
	if v, ok := args["priority"]; ok && v != nil {
		priority, err := validatePriority(v)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		update["priority"] = priority
	}
	if v, ok := args["labels"]; ok && v != nil {
		labels, err := validateLabels(v)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		update["labels"] = labels
	}
	if len(update) == 0 {
		return mcp.NewToolResultError("at least one update field is required"), nil
	}

	data, err := s.client.UpdateTask(ctx, taskID, update)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(formatJSON(data)), nil
}

func (s *Server) deleteTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	taskID := strParam(args, "task_id")
	if strings.TrimSpace(taskID) == "" {
		return mcp.NewToolResultError("task_id is required and must not be blank"), nil
	}
	if err := s.client.DeleteTask(ctx, taskID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Deleted task %s", taskID)), nil
}

func (s *Server) completeTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	taskID := strParam(args, "task_id")
	if strings.TrimSpace(taskID) == "" {
		return mcp.NewToolResultError("task_id is required and must not be blank"), nil
	}
	if err := s.client.CompleteTask(ctx, taskID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Completed task %s", taskID)), nil
}

// --- helpers ---

func strParam(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func intParam(args map[string]interface{}, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		if math.Trunc(n) != n {
			return 0
		}
		return int(n)
	case int:
		return n
	}
	return def
}

func validatePriority(raw interface{}) (int, error) {
	var priority int
	switch n := raw.(type) {
	case float64:
		if math.Trunc(n) != n {
			return 0, fmt.Errorf("priority must be an integer between 1 and 4")
		}
		priority = int(n)
	case int:
		priority = n
	default:
		return 0, fmt.Errorf("priority must be an integer between 1 and 4")
	}
	if priority < 1 || priority > 4 {
		return 0, fmt.Errorf("priority must be an integer between 1 and 4")
	}
	return priority, nil
}

func validateLabels(raw interface{}) ([]string, error) {
	values, ok := raw.([]interface{})
	if !ok {
		if stringsValue, ok := raw.([]string); ok {
			values = make([]interface{}, len(stringsValue))
			for i, label := range stringsValue {
				values[i] = label
			}
		} else {
			return nil, fmt.Errorf("labels must be an array of strings")
		}
	}
	if len(values) > maxLabels {
		return nil, fmt.Errorf("labels must contain at most %d entries", maxLabels)
	}
	labels := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for i, rawLabel := range values {
		label, ok := rawLabel.(string)
		if !ok || strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("labels[%d] must be a nonblank string", i)
		}
		if len([]rune(label)) > maxLabelLength {
			return nil, fmt.Errorf("labels[%d] must be at most %d characters", i, maxLabelLength)
		}
		if _, exists := seen[label]; exists {
			return nil, fmt.Errorf("labels must not contain duplicates")
		}
		seen[label] = struct{}{}
		labels[i] = label
	}
	return labels, nil
}

func formatJSON(data []byte) string {
	var parsed interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return string(data)
	}
	pretty, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(pretty)
}
