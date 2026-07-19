package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const defaultHTTPTimeout = 30 * time.Second
const maxResponseBodyBytes int64 = 16 << 20

type Client struct {
	url        string
	collection string
	httpClient *http.Client
}

func NewClient(url, collection string) *Client {
	return &Client{
		url:        url,
		collection: collection,
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Point represents a Qdrant point with vector and payload.
type Point struct {
	ID      string                 `json:"id"`
	Vector  []float32              `json:"vector,omitempty"`
	Payload map[string]interface{} `json:"payload,omitempty"`
	Score   float64                `json:"score,omitempty"`
}

// parsePointID converts a Qdrant point ID (int or string) to string.
func parsePointID(v interface{}) string {
	switch id := v.(type) {
	case string:
		return id
	case exactPointID:
		return string(id)
	case float64:
		return strconv.FormatFloat(id, 'f', -1, 64)
	case json.Number:
		return id.String()
	default:
		return fmt.Sprintf("%v", id)
	}
}

// exactPointID decodes Qdrant's string or unsigned integer IDs without routing
// JSON numbers through float64. It is intentionally used only for ID fields so
// numeric values in point payloads retain their established float64 types.
type exactPointID string

func (id *exactPointID) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty point ID")
	}
	if data[0] == '"' {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		*id = exactPointID(decoded)
		return nil
	}
	if _, err := strconv.ParseUint(string(data), 10, 64); err != nil {
		return fmt.Errorf("invalid numeric point ID %q: %w", data, err)
	}
	*id = exactPointID(data)
	return nil
}

func qdrantPointID(id string) interface{} {
	if parsed, err := strconv.ParseUint(id, 10, 64); err == nil {
		return parsed
	}
	return id
}

// Get retrieves one point by its exact ID. The bool is false when Qdrant
// reports that the point does not exist.
func (c *Client) Get(ctx context.Context, id string) (Point, bool, error) {
	requestURL := fmt.Sprintf("%s/collections/%s/points/%s?with_vector=true", c.url, c.collection, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return Point{}, false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Point{}, false, fmt.Errorf("get point: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Point{}, false, nil
	}
	b, err := readLimitedBody(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return Point{}, false, fmt.Errorf("read point response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return Point{}, false, fmt.Errorf("GET %s failed (status %d): %s", requestURL, resp.StatusCode, string(b))
	}
	var result struct {
		Result struct {
			ID      exactPointID           `json:"id"`
			Vector  []float32              `json:"vector"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return Point{}, false, fmt.Errorf("decode point response: %w", err)
	}
	return Point{ID: parsePointID(result.Result.ID), Vector: result.Result.Vector, Payload: result.Result.Payload}, true, nil
}

// EnsureCollection creates the collection if it doesn't exist.
func (c *Client) EnsureCollection(ctx context.Context, vectorSize int) error {
	// Check if collection exists.
	url := fmt.Sprintf("%s/collections/%s", c.url, c.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("check collection: %w", err)
	}
	checkBody, readErr := readLimitedBody(resp.Body, maxResponseBodyBytes)
	resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read collection check response: %w", readErr)
	}

	if resp.StatusCode == http.StatusOK {
		return nil // already exists
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("GET %s failed (status %d): %s", url, resp.StatusCode, string(checkBody))
	}

	// Create collection.
	body := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	return c.mutate(ctx, http.MethodPut, url, body, false)
}

// Upsert inserts or updates a point.
func (c *Client) Upsert(ctx context.Context, point Point) error {
	url := fmt.Sprintf("%s/collections/%s/points", c.url, c.collection)
	body := map[string]interface{}{
		"points": []map[string]interface{}{
			{
				"id":      point.ID,
				"vector":  point.Vector,
				"payload": point.Payload,
			},
		},
	}
	return c.mutate(ctx, http.MethodPut, url, body, true)
}

// Search performs a vector similarity search with optional filters.
func (c *Client) Search(ctx context.Context, vector []float32, limit int, filters map[string]interface{}, scoreThreshold *float64) ([]Point, error) {
	url := fmt.Sprintf("%s/collections/%s/points/search", c.url, c.collection)
	body := map[string]interface{}{
		"vector":       vector,
		"limit":        limit,
		"with_payload": true,
	}
	if filters != nil {
		body["filter"] = filters
	}
	if scoreThreshold != nil {
		body["score_threshold"] = *scoreThreshold
	}

	respBody, err := c.postJSON(ctx, url, body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result []struct {
			ID      exactPointID           `json:"id"`
			Score   float64                `json:"score"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	points := make([]Point, len(result.Result))
	for i, r := range result.Result {
		points[i] = Point{
			ID:      parsePointID(r.ID),
			Score:   r.Score,
			Payload: r.Payload,
		}
	}
	return points, nil
}

// ScrollResult holds a page of scroll results.
type ScrollResult struct {
	Points    []ScrollPoint `json:"points"`
	RawOffset interface{}   `json:"-"`
}

func (r *ScrollResult) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Points    []ScrollPoint   `json:"points"`
		RawOffset json.RawMessage `json:"next_page_offset"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	r.Points = decoded.Points
	r.RawOffset = nil
	raw := bytes.TrimSpace(decoded.RawOffset)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] == '"' {
		var offset string
		if err := json.Unmarshal(raw, &offset); err != nil {
			return err
		}
		r.RawOffset = offset
		return nil
	}
	if _, err := strconv.ParseUint(string(raw), 10, 64); err != nil {
		return fmt.Errorf("invalid numeric scroll offset %q: %w", raw, err)
	}
	r.RawOffset = json.Number(string(raw))
	return nil
}

// ScrollPoint is a point returned by scroll (may include vector).
type ScrollPoint struct {
	ID      string                 `json:"-"`
	RawID   exactPointID           `json:"id"`
	Vector  []float32              `json:"vector,omitempty"`
	Payload map[string]interface{} `json:"payload,omitempty"`
}

// Scroll paginates through points with full payload.
func (c *Client) Scroll(ctx context.Context, limit int, offset interface{}, filters map[string]interface{}, withVector bool) (*ScrollResult, error) {
	return c.ScrollWithPayload(ctx, limit, offset, filters, true, withVector)
}

// ScrollWithPayload is like Scroll but accepts an explicit payload selector.
// withPayload may be: true (all fields), false (none), or []string (specific fields).
func (c *Client) ScrollWithPayload(ctx context.Context, limit int, offset interface{}, filters map[string]interface{}, withPayload interface{}, withVector bool) (*ScrollResult, error) {
	url := fmt.Sprintf("%s/collections/%s/points/scroll", c.url, c.collection)
	body := map[string]interface{}{
		"limit":        limit,
		"with_payload": withPayload,
		"with_vector":  withVector,
	}
	if offset != nil {
		body["offset"] = offset
	}
	if filters != nil {
		body["filter"] = filters
	}

	respBody, err := c.postJSON(ctx, url, body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result ScrollResult `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode scroll response: %w", err)
	}

	for i := range result.Result.Points {
		result.Result.Points[i].ID = parsePointID(result.Result.Points[i].RawID)
	}

	return &result.Result, nil
}

// ScrollAll retrieves all points with full payload.
func (c *Client) ScrollAll(ctx context.Context, filters map[string]interface{}, withVector bool) ([]ScrollPoint, error) {
	return c.ScrollAllWithPayload(ctx, filters, true, withVector)
}

// ScrollAllWithPayload paginates through all points with an explicit payload selector.
func (c *Client) ScrollAllWithPayload(ctx context.Context, filters map[string]interface{}, withPayload interface{}, withVector bool) ([]ScrollPoint, error) {
	var all []ScrollPoint
	var offset interface{}
	for {
		result, err := c.ScrollWithPayload(ctx, 100, offset, filters, withPayload, withVector)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Points...)
		if result.RawOffset == nil {
			break
		}
		offset = result.RawOffset
	}
	return all, nil
}

// Delete removes points by IDs.
func (c *Client) Delete(ctx context.Context, ids []string) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", c.url, c.collection)
	points := make([]interface{}, len(ids))
	for i, id := range ids {
		points[i] = qdrantPointID(id)
	}
	body := map[string]interface{}{
		"points": points,
	}
	return c.mutate(ctx, http.MethodPost, url, body, true)
}

// DeleteByFilter removes all points matching the filter in a single request.
func (c *Client) DeleteByFilter(ctx context.Context, filter map[string]interface{}) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", c.url, c.collection)
	body := map[string]interface{}{
		"filter": filter,
	}
	return c.mutate(ctx, http.MethodPost, url, body, true)
}

// CreateFieldIndex creates a payload field index for fast filtering.
func (c *Client) CreateFieldIndex(ctx context.Context, fieldName, fieldSchema string) error {
	url := fmt.Sprintf("%s/collections/%s/index", c.url, c.collection)
	body := map[string]interface{}{
		"field_name":   fieldName,
		"field_schema": fieldSchema,
	}
	return c.mutate(ctx, http.MethodPut, url, body, true)
}

// SetPayload updates payload fields on a point without re-embedding.
func (c *Client) SetPayload(ctx context.Context, id string, payload map[string]interface{}) error {
	url := fmt.Sprintf("%s/collections/%s/points/payload", c.url, c.collection)
	body := map[string]interface{}{
		"payload": payload,
		"points":  []interface{}{qdrantPointID(id)},
	}
	return c.mutate(ctx, http.MethodPost, url, body, true)
}

// CreateSnapshot triggers a snapshot creation.
func (c *Client) CreateSnapshot(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/collections/%s/snapshots", c.url, c.collection)
	respBody, err := c.postJSON(ctx, url, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode snapshot response: %w", err)
	}
	if err := validateMutationResponse(respBody, false); err != nil {
		return "", fmt.Errorf("create snapshot: %w", err)
	}
	if result.Result.Name == "" {
		return "", fmt.Errorf("create snapshot: qdrant response did not include a snapshot name")
	}
	return result.Result.Name, nil
}

// ListSnapshots returns all snapshot names.
func (c *Client) ListSnapshots(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("%s/collections/%s/snapshots", c.url, c.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, err := readLimitedBody(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s failed (status %d): %s", url, resp.StatusCode, string(b))
	}

	var result struct {
		Result []struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}

	names := make([]string, len(result.Result))
	for i, s := range result.Result {
		names[i] = s.Name
	}
	return names, nil
}

// DeleteSnapshot removes a snapshot by name.
func (c *Client) DeleteSnapshot(ctx context.Context, name string) error {
	requestURL := fmt.Sprintf("%s/collections/%s/snapshots/%s", c.url, c.collection, url.PathEscape(name))
	return c.mutate(ctx, http.MethodDelete, requestURL, nil, false)
}

// --- HTTP helpers ---

func (c *Client) mutate(ctx context.Context, method, requestURL string, body interface{}, wait bool) error {
	if wait {
		parsed, err := url.Parse(requestURL)
		if err != nil {
			return fmt.Errorf("parse mutation URL: %w", err)
		}
		query := parsed.Query()
		query.Set("wait", "true")
		parsed.RawQuery = query.Encode()
		requestURL = parsed.String()
	}

	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, requestURL, err)
	}
	defer resp.Body.Close()
	respBody, err := readLimitedBody(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, requestURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed (status %d): %s", method, requestURL, resp.StatusCode, string(respBody))
	}
	if err := validateMutationResponse(respBody, wait); err != nil {
		return fmt.Errorf("%s %s: %w", method, requestURL, err)
	}
	return nil
}

func validateMutationResponse(body []byte, requireCompleted bool) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("empty qdrant mutation response")
	}
	var response struct {
		Status interface{} `json:"status"`
		Result interface{} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decode qdrant mutation response: %w", err)
	}

	validated := false
	if response.Status != nil {
		status, ok := response.Status.(string)
		if !ok || status != "ok" {
			return fmt.Errorf("qdrant mutation status is %v, want ok", response.Status)
		}
		validated = true
	}
	completed := false
	if result, ok := response.Result.(map[string]interface{}); ok {
		if rawStatus, exists := result["status"]; exists {
			status, ok := rawStatus.(string)
			if !ok || status != "completed" {
				return fmt.Errorf("qdrant operation status is %v, want completed", rawStatus)
			}
			completed = true
			validated = true
		}
	}
	if requireCompleted && !completed {
		return fmt.Errorf("qdrant mutation response does not confirm a completed operation")
	}
	if !validated {
		return fmt.Errorf("qdrant mutation response contains no verifiable status")
	}
	return nil
}

func (c *Client) postJSON(ctx context.Context, url string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := readLimitedBody(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("POST %s failed (status %d): %s", url, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func readLimitedBody(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return body, nil
}

func (c *Client) postDiscard(ctx context.Context, url string, body interface{}) error {
	_, err := c.postJSON(ctx, url, body)
	return err
}
