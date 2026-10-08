package qdrant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SetGrouping changes only grouping metadata, retaining all lifecycle and vector fields.
func (c *Client) SetGrouping(ctx context.Context, id string, tags []string, primary, audit string) error {
	if err := validateMaintenanceTarget(id); err != nil {
		return err
	}
	if len(tags) > 100 || len(primary) > 255 || len(audit) > 128 {
		return errors.New("invalid grouping limits")
	}
	has := primary == ""
	seen := map[string]bool{}
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 255 || seen[tag] {
			return errors.New("invalid grouping tags")
		}
		seen[tag] = true
		if tag == primary {
			has = true
		}
	}
	if !has {
		return errors.New("primary grouping missing from tags")
	}
	endpoint := fmt.Sprintf("%s/collections/%s/points/payload?wait=true&ordering=strong", c.url, c.collection)
	return c.mutate(ctx, http.MethodPost, endpoint, map[string]interface{}{
		"points":  []interface{}{qdrantPointID(id)},
		"payload": map[string]interface{}{"tags": tags, "primary_tag": primary, "ai_grouping_ref": audit},
	}, true, false)
}
