package http

import (
	"bytes"
	"testing"
)

func TestBuildPrefixPostfixTrimmedStr(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty body",
			input:    []byte{},
			expected: "",
		},
		{
			name:     "body shorter than 200 chars",
			input:    []byte("hello world"),
			expected: "hello world",
		},
		{
			name:     "body exactly 200 chars",
			input:    bytes.Repeat([]byte("a"), 200),
			expected: string(bytes.Repeat([]byte("a"), 200)),
		},
		{
			name:     "body longer than 200 chars - prefix and suffix",
			input:    append(bytes.Repeat([]byte("a"), 150), bytes.Repeat([]byte("b"), 150)...),
			expected: string(bytes.Repeat([]byte("a"), 100)) + " ..... " + string(bytes.Repeat([]byte("b"), 100)),
		},
		{
			name:     "body with special characters",
			input:    []byte("Hello, 世界! This is a test with special chars: @#$%^&*()"),
			expected: "Hello, 世界! This is a test with special chars: @#$%^&*()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildPrefixPostfixTrimmedStr(tt.input)
			if result != tt.expected {
				t.Errorf("buildPrefixPostfixTrimmedStr(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
