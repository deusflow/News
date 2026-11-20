package groq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerate_ResponseFormatByProvider(t *testing.T) {
	tests := []struct {
		name               string
		providerName       string
		wantResponseFormat bool
	}{
		{
			name:               "groq includes json_object response_format",
			providerName:       "groq",
			wantResponseFormat: true,
		},
		{
			name:               "openrouter omits response_format for free models compatibility",
			providerName:       "openrouter",
			wantResponseFormat: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &captured)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"danish\":\"Dansk\",\"ukrainian\":\"Укр\",\"tldr\":\"TLDR\",\"mood\":\"neutral\",\"audience_score\":5}"}}]}`))
			}))
			defer srv.Close()

			client := NewClientWithOptions(tt.providerName, "test-key", "test-model", srv.URL)
			resp, err := client.Generate(context.Background(), "Title", "Content", "sys", "user")
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if resp.Danish != "Dansk" {
				t.Fatalf("unexpected response: %+v", resp)
			}

			_, hasRespFmt := captured["response_format"]
			if hasRespFmt != tt.wantResponseFormat {
				t.Errorf("has response_format = %v, want %v (payload: %v)", hasRespFmt, tt.wantResponseFormat, captured)
			}
		})
	}
}

func TestGenerate_RetriesWithoutResponseFormatOnJSONValidateFailed(t *testing.T) {
	attempts := 0
	var seenRespFmt []bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		_, hasRespFmt := payload["response_format"]
		seenRespFmt = append(seenRespFmt, hasRespFmt)

		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Failed to generate JSON. Please adjust your prompt.","type":"invalid_request_error","code":"json_validate_failed"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"<think>reasoning</think>\n{\"danish\":\"Dansk\",\"ukrainian\":\"Укр\",\"tldr\":\"TLDR\",\"mood\":\"neutral\",\"audience_score\":7}"}}]}`))
	}))
	defer srv.Close()

	client := NewClientWithOptions("groq", "test-key", "openai/gpt-oss-120b", srv.URL)
	resp, err := client.Generate(context.Background(), "Title", "Content", "sys", "user")
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if !seenRespFmt[0] || seenRespFmt[1] {
		t.Fatalf("expected first request with response_format and retry without it, got %v", seenRespFmt)
	}
	if resp.Danish != "Dansk" || resp.AudienceScore != 7 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestGenerateRaw_UsesLowReasoningEffort(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"1, 3"}}]}`))
	}))
	defer srv.Close()

	client := NewClientWithOptions("groq", "test-key", "openai/gpt-oss-120b", srv.URL)
	raw, err := client.GenerateRaw(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("GenerateRaw() error = %v", err)
	}
	if raw != "1, 3" {
		t.Fatalf("unexpected raw = %q", raw)
	}
	if got := captured["reasoning_effort"]; got != "low" {
		t.Errorf("expected reasoning_effort=low for GenerateRaw, got %v", got)
	}
}

func TestGenerate_GptOssDefaultsToLowReasoningAndAdequateTokenBudget(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"danish\":\"Dansk\",\"ukrainian\":\"Укр\",\"tldr\":\"TLDR\",\"mood\":\"neutral\",\"audience_score\":7}"}}]}`))
	}))
	defer srv.Close()

	client := NewClientWithOptions("groq", "test-key", "openai/gpt-oss-120b", srv.URL)
	if _, err := client.Generate(context.Background(), "Title", "Content", "sys", "user"); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got := captured["reasoning_effort"]; got != "low" {
		t.Errorf("expected reasoning_effort=low for gpt-oss-120b Generate, got %v", got)
	}
	if got, ok := captured["max_completion_tokens"].(float64); !ok || got < 3072 {
		t.Errorf("expected max_completion_tokens >= 3072, got %v", captured["max_completion_tokens"])
	}
}

