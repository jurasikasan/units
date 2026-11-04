package textutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripCodeFence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "json fenced block",
			in:   "```json\n[\n\t{\n\t\t\"debit\": \"1000.00\"\n\t}\n]\n```",
			want: "[\n\t{\n\t\t\"debit\": \"1000.00\"\n\t}\n]",
		},
		{
			name: "languageless fenced block",
			in:   "```\n{\"is_valid\": true}\n```",
			want: "{\"is_valid\": true}",
		},
		{
			name: "no fence passes through",
			in:   "[{\"account\": \"1100\"}]",
			want: "[{\"account\": \"1100\"}]",
		},
		{
			name: "surrounding whitespace is trimmed",
			in:   "  \n```json\n[1, 2, 3]\n```\n  ",
			want: "[1, 2, 3]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripCodeFence(tt.in))
		})
	}
}
