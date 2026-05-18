package auth

import (
	"strings"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	key := GenerateAPIKey()
	if !strings.HasPrefix(key, keyPrefix) {
		t.Errorf("expected key to start with %s, got %s", keyPrefix, key)
	}
	if len(key) <= len(keyPrefix) {
		t.Errorf("expected key longer than prefix, got %s", key)
	}

	// Uniqueness
	key2 := GenerateAPIKey()
	if key == key2 {
		t.Error("expected unique keys")
	}
}

func TestParseBearerToken(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Bearer sk-test", "sk-test"},
		{"Bearer  token-with-space", "token-with-space"},
		{"Basic sk-test", ""},
		{"", ""},
		{"Bearer", ""},
	}

	for _, tt := range tests {
		result := ParseBearerToken(tt.input)
		if result != tt.expected {
			t.Errorf("ParseBearerToken(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}
