package config

import (
	"reflect"
	"testing"
)

func TestNormalizeProviders(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "default fallback when empty",
			input:    []string{},
			expected: []string{"gemini", "groq"},
		},
		{
			name:     "custom multi-provider chain preserved",
			input:    []string{"gemini", "cerebras", "groq", "openrouter"},
			expected: []string{"gemini", "cerebras", "groq", "openrouter"},
		},
		{
			name:     "gemini-flash-lite, openai, and qwen supported",
			input:    []string{"gemini-flash-lite", "openai", "qwen"},
			expected: []string{"gemini-flash-lite", "openai", "qwen"},
		},
		{
			name:     "unknown providers filtered out and duplicates deduped",
			input:    []string{"gemini", "unknown_provider", "cerebras", "gemini"},
			expected: []string{"gemini", "cerebras"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeProviders(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("normalizeProviders() = %v, want %v", got, tt.expected)
			}
		})
	}
}
