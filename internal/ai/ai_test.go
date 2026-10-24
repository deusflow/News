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
	calls       int
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockProvider) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	m.calls++
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

func TestManager_Cooldown(t *testing.T) {
	t.Setenv("AI_REQUEST_DELAY_SECONDS", "0")
	m := metrics.New()

	p1 := &mockProvider{
		name: "exhausted_gemini",
		err:  errors.New("429 RESOURCE_EXHAUSTED quota exceeded"),
	}
	p2 := &mockProvider{
		name: "fallback_groq",
		response: &ai.Response{
			Danish:        "Dansk 2",
			Ukrainian:     "Текст 2",
			TLDR:          "TLDR 2",
			Mood:          "neutral",
			AudienceScore: 5,
		},
	}

	mgr := ai.NewManager(m, 10, p1, p2)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Call 1: p1 fails with RESOURCE_EXHAUSTED, falls back to p2, marks p1 on cooldown
	resp1, err := mgr.Generate(ctx, "Title 1", "Content 1", "sys", "user")
	if err != nil {
		t.Fatalf("First Generate() failed: %v", err)
	}
	if resp1.Danish != "Dansk 2" {
		t.Errorf("Expected p2 response, got: %+v", resp1)
	}
	if p1.calls != 1 {
		t.Errorf("Expected p1 to be called once, got %d", p1.calls)
	}

	// Call 2: p1 is on cooldown, should NOT be called at all!
	resp2, err := mgr.Generate(ctx, "Title 2", "Content 2", "sys", "user")
	if err != nil {
		t.Fatalf("Second Generate() failed: %v", err)
	}
	if resp2.Danish != "Dansk 2" {
		t.Errorf("Expected p2 response, got: %+v", resp2)
	}
	if p1.calls != 1 {
		t.Errorf("Expected p1 to NOT be called on second attempt due to cooldown, got %d calls", p1.calls)
	}
	if p2.calls != 2 {
		t.Errorf("Expected p2 to be called twice, got %d calls", p2.calls)
	}
}
