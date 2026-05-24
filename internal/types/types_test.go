package types

import "testing"

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"sr-abc123def456", "****f456"},
		{"sk-ant-api03-test123", "****t123"},
		{"short", "****hort"},
		{"", "****"},
		{"ab", "****"},
		{"exactly8", "****tly8"},
		{"sr-", "****"},
		{"123456789", "****6789"},
	}
	for _, tt := range tests {
		got := MaskAPIKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
