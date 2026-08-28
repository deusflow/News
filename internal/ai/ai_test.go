package ai_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/metrics"
)

type mockProvider struct {
	name        string
	response    *ai.Response
	rawResponse string
	err         error
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockProvider) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.rawResponse, nil
}

func (m *mockProvider) Close() {}

func TestResponse_Validate(t *testing.T) {
	tests := []struct {
		name      string
		resp      ai.Response
		wantErr   bool
		checkMood string
		checkAud  int
	}{
		{
			name: "valid response",
			resp: ai.Response{
				Danish:        "Dansk tekst.",
				Ukrainian:     "Український текст.",
				TLDR:          "TLDR заголовок",
				Mood:          "Positive",
				Category:      "Work",
				AudienceScore: 8,
			},
			wantErr:   false,
			checkMood: "positive",
			checkAud:  8,
		},
		{
			name: "missing danish",
			resp: ai.Response{
				Ukrainian: "Текст",
				TLDR:      "TLDR",
			},
			wantErr: true,
		},
		{
			name: "missing ukrainian",
			resp: ai.Response{
				Danish: "Dansk",
				TLDR:   "TLDR",
			},
			wantErr: true,
		},
		{
			name: "missing tldr",
			resp: ai.Response{
				Danish:    "Dansk",
				Ukrainian: "Текст",
			},
			wantErr: true,
		},
		{
			name: "invalid mood becomes neutral",
			resp: ai.Response{
				Danish:        "Dansk",
				Ukrainian:     "Текст",
				TLDR:          "TLDR",
				Mood:          "random_mood",
				AudienceScore: 0,
			},
			wantErr:   false,
			checkMood: "neutral",
			checkAud:  1, // clamped from 0 to 1
		},
		{
			name: "audience score clamped to 12",
			resp: ai.Response{
				Danish:        "Dansk",
				Ukrainian:     "Текст",
				TLDR:          "TLDR",
				Mood:          "urgent",
				AudienceScore: 25,
			},
			wantErr:   false,
			checkMood: "urgent",
			checkAud:  12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resp.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if tt.resp.Mood != tt.checkMood {
					t.Errorf("Mood = %q, want %q", tt.resp.Mood, tt.checkMood)
				}
				if tt.resp.AudienceScore != tt.checkAud {
					t.Errorf("AudienceScore = %d, want %d", tt.resp.AudienceScore, tt.checkAud)
				}
			}
		})
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	tests := []struct {
		input   string
		wantSec int
		wantOk  bool
	}{
		{"Rate limit exceeded. Retry after 15s", 15, true},
		{"Please wait 60 seconds before next request", 60, true},
		{"RESOURCE_EXHAUSTED: retry_delay: 8s", 8, true},
		{"Generic internal error", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			sec, ok := ai.ParseRetryAfterSeconds(tt.input)
			if ok != tt.wantOk {
				t.Errorf("ParseRetryAfterSeconds(%q) ok = %v, want %v", tt.input, ok, tt.wantOk)
			}
			if ok && sec != tt.wantSec {
				t.Errorf("ParseRetryAfterSeconds(%q) = %d, want %d", tt.input, sec, tt.wantSec)
			}
		})
	}
}

func TestManager_Fallback(t *testing.T) {
	m := metrics.New()

	p1 := &mockProvider{
		name: "primary",
		err:  errors.New("429 rate limit exceeded"),
	}
	p2 := &mockProvider{
		name: "secondary",
		response: &ai.Response{
			Danish:        "Dansk",
			Ukrainian:     "Текст",
			TLDR:          "TLDR",
			Mood:          "neutral",
			AudienceScore: 5,
		},
	}

	mgr := ai.NewManager(m, 10, p1, p2)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := mgr.Generate(ctx, "Test Title", "Content", "sys", "user")
	if err != nil {
		t.Fatalf("Generate() fallback failed: %v", err)
	}
	if resp.Danish != "Dansk" {
		t.Errorf("Expected secondary response, got: %+v", resp)
	}
}
