package types

import "testing"

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"sr-abc123def456", "sr-a****f456"},
		{"sk-ant-api03-test123", "sk-a****t123"},
		{"short", "****"},
		{"", "****"},
		{"ab", "****"},
		{"exactly8", "****"},
		{"sr-", "****"},
		{"123456789", "1234****6789"},
	}
	for _, tt := range tests {
		got := MaskAPIKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
