package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func DiscoverJWKSURL(ctx context.Context, issuer string) (string, error) {
	metadataURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openid discovery failed: %s", resp.Status)
	}

	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("openid discovery at %s did not include jwks_uri", metadataURL)
	}
	return doc.JWKSURI, nil
}
