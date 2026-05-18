package auth

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
)

const keyPrefix = "sr-"

// GenerateAPIKey creates a new random API key with format sr-<base64url>.
func GenerateAPIKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// Fallback: this should never happen in practice
		panic("failed to generate random bytes: " + err.Error())
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseBearerToken extracts the token from an Authorization header.
// Expected format: "Bearer <token>". Returns empty string if invalid.
func ParseBearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
