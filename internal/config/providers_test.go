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
		{
			name:     "provider:model syntax with multiple Groq models for rate limit fallback",
			input:    []string{"gemini", "groq:openai/gpt-oss-120b", "groq:qwen/qwen3.8-27b", "groq:openai/gpt-oss-120b"},
			expected: []string{"gemini", "groq:openai/gpt-oss-120b", "groq:qwen/qwen3.8-27b"},
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

func TestAllowedModelDefaults(t *testing.T) {
	// Whitelist of active models verified in official documentation
	allowedGroqModels := map[string]bool{
		"openai/gpt-oss-120b": true,
		"openai/gpt-oss-20b":  true,
		"qwen/qwen3.8-27b":    true,
	}

	// 1. Check DefaultGroqModel
	if !allowedGroqModels[DefaultGroqModel] {
		t.Errorf("DefaultGroqModel %q is not in allowed active Groq models whitelist: %v", DefaultGroqModel, allowedGroqModels)
	}

	// 2. Check default providers from normalizeProviders
	defaultProviders := normalizeProviders(nil)
	if len(defaultProviders) == 0 {
		t.Fatalf("Default providers should not be empty")
	}

	allowedProviders := map[string]bool{
		"gemini":            true,
		"gemini-flash-lite": true,
		"groq":              true,
		"cerebras":          true,
		"openrouter":        true,
		"openai":            true,
		"qwen":              true,
	}

	for _, p := range defaultProviders {
		if !allowedProviders[p] {
			t.Errorf("Default provider %q is not in allowed providers whitelist", p)
		}
	}

	// 3. Ensure no legacy or deprecated keywords exist in model defaults
	forbiddenSubstrings := []string{"llama", "versatile", "instant"}
	for _, forbidden := range forbiddenSubstrings {
		if containsWholeWord(DefaultGroqModel, forbidden) {
			t.Errorf("DefaultGroqModel %q contains deprecated keyword %q", DefaultGroqModel, forbidden)
		}
	}
}

