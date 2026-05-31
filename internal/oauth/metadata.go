package oauth

import (
	"encoding/json"
	"net/http"
	"strings"
)

const protectedResourcePath = "/.well-known/oauth-protected-resource"

type ProtectedResourceMetadata struct {
	Resource              string   `json:"resource"`
	AuthorizationServers  []string `json:"authorization_servers"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
	ResourceDocumentation string   `json:"resource_documentation,omitempty"`
}

type MetadataConfig struct {
	Resource              string
	AuthorizationServers  []string
	Scopes                []string
	ResourceDocumentation string
}

func NewProtectedResourceMetadata(cfg MetadataConfig) ProtectedResourceMetadata {
	return ProtectedResourceMetadata{
		Resource:              strings.TrimRight(cfg.Resource, "/"),
		AuthorizationServers:  cfg.AuthorizationServers,
		ScopesSupported:       cfg.Scopes,
		ResourceDocumentation: cfg.ResourceDocumentation,
	}
}

func MetadataURL(resource string) string {
	return strings.TrimRight(resource, "/") + protectedResourcePath
}

func Challenge(resource string, scopes []string) string {
	parts := []string{`resource_metadata="` + MetadataURL(resource) + `"`}
	if len(scopes) > 0 {
		parts = append(parts, `scope="`+strings.Join(scopes, " ")+`"`)
	}
	return "Bearer " + strings.Join(parts, ", ")
}

func MetadataHandler(metadata ProtectedResourceMetadata) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(metadata)
	}
}
