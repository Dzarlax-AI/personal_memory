package memory

import (
	"crypto/sha1"
	"fmt"
	"strings"
)

const defaultNamespace = "default"

// NormalizeNamespace returns the canonical namespace used for storage and IDs.
func NormalizeNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return defaultNamespace
	}
	return namespace
}

// PointID returns a namespace-aware deterministic UUID-shaped ID for a fact.
// The version prefix and NUL separators make the input encoding unambiguous and
// leave room for a future ID scheme without colliding with this one.
func PointID(namespace, text string) string {
	h := sha1.New()
	h.Write([]byte("personal-memory-point-id:v2\x00"))
	h.Write([]byte(NormalizeNamespace(namespace)))
	h.Write([]byte{0})
	h.Write([]byte(text))
	b := h.Sum(nil)
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
