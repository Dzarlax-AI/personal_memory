package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// ScrollForMigration preserves arbitrary payload numbers exactly. Ordinary
// runtime reads retain their existing float64 contract; a backfill must not
// round large numeric provenance/metadata values before writing them back.
func (c *Client) ScrollForMigration(ctx context.Context, limit int, offset any, withVector bool) (*ScrollResult, error) {
	body := map[string]any{"limit": limit, "with_payload": true, "with_vector": withVector}
	if offset != nil {
		body["offset"] = offset
	}
	raw, err := c.postJSON(ctx, fmt.Sprintf("%s/collections/%s/points/scroll", c.url, c.collection), body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result *struct {
			Points *[]struct {
				ID      exactPointID   `json:"id"`
				Payload map[string]any `json:"payload"`
				Vector  []float32      `json:"vector"`
			} `json:"points"`
			Next any `json:"next_page_offset"`
		} `json:"result"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("invalid migration scroll response")
	}
	if envelope.Result == nil || envelope.Result.Points == nil {
		return nil, fmt.Errorf("missing migration scroll result")
	}
	out := &ScrollResult{RawOffset: envelope.Result.Next, Points: make([]ScrollPoint, 0, len(*envelope.Result.Points))}
	for _, p := range *envelope.Result.Points {
		if p.ID == "" {
			return nil, fmt.Errorf("missing migration point identity")
		}
		out.Points = append(out.Points, ScrollPoint{ID: string(p.ID), RawID: p.ID, Vector: p.Vector, Payload: p.Payload})
	}
	return out, nil
}
