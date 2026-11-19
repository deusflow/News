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
		{"Rate limit reached on tokens per minute (TPM): Please try again in 26.5875s.", 27, true},
		{"Please try again in 3.0525s", 3, true},
		{"Please try again in 1m10s", 70, true},
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

func TestSanitizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain json object",
			input:    `{"summary": "test", "mood": "neutral"}`,
			expected: `{"summary": "test", "mood": "neutral"}`,
		},
		{
			name: "wrapped in markdown fence with json tag",
			input: "```json\n{\n  \"summary\": \"hello\"\n}\n```",
			expected: "{\n  \"summary\": \"hello\"\n}",
		},
		{
			name: "wrapped in markdown fence without lang tag",
			input: "```\n{\n  \"summary\": \"hello\"\n}\n```",
			expected: "{\n  \"summary\": \"hello\"\n}",
		},
		{
			name: "with preamble and epilogue text",
			input: "Here is your JSON response:\n```json\n{\"summary\": \"test\"}\n```\nHope this helps!",
			expected: `{"summary": "test"}`,
		},
		{
			name: "json array with text around it",
			input: "Selected indices:\n```json\n[0, 5, 12]\n```\nDone.",
			expected: `[0, 5, 12]`,
		},
		{
			name:     "strips think block containing braces before json object",
			input:    "<think>Consider draft {bad} and indices [1, 2].</think>\n```json\n{\"danish\":\"Dansk\",\"ukrainian\":\"Укр\"}\n```",
			expected: `{"danish":"Dansk","ukrainian":"Укр"}`,
		},
		{
			name:     "strips think block containing brackets before triage json",
			input:    "<think>Ignoring [99] and keeping [1, 3].</think>\n{\"selected\":[1,3]}",
			expected: `{"selected":[1,3]}`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ai.SanitizeJSON(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeJSON() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestManager_PerProviderDelay(t *testing.T) {
	t.Setenv("AI_REQUEST_DELAY_SECONDS", "1")
	m := metrics.New()

	p1 := &mockProvider{
		name: "gemini",
		err:  errors.New("429 RESOURCE_EXHAUSTED quota exceeded"),
	}
	p2 := &mockProvider{
		name: "groq",
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call 1: gemini fails and falls back to groq immediately (no 1s wait between different providers)
	start := time.Now()
	if _, err := mgr.Generate(ctx, "T1", "C1", "sys", "user"); err != nil {
		t.Fatalf("Call 1 failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Errorf("Expected immediate fallback across providers, took %v", elapsed)
	}

	// Call 2: gemini is on cooldown, groq is called a second time -> must wait ~1s
	start2 := time.Now()
	if _, err := mgr.Generate(ctx, "T2", "C2", "sys", "user"); err != nil {
		t.Fatalf("Call 2 failed: %v", err)
	}
	if elapsed2 := time.Since(start2); elapsed2 < 800*time.Millisecond {
		t.Errorf("Expected ~1s per-provider delay for consecutive groq calls, took %v", elapsed2)
	}
}

type flakyShortRateLimitProvider struct {
	name     string
	calls    int
	response *ai.Response
}

func (f *flakyShortRateLimitProvider) Name() string { return f.name }
func (f *flakyShortRateLimitProvider) Close()       {}
func (f *flakyShortRateLimitProvider) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	f.calls++
	if f.calls == 1 {
		return nil, ai.NewRateLimitedError(
			f.name,
			errors.New(`groq api error 429: {"error":{"message":"Rate limit reached on tokens per minute (TPM). Please try again in 1.0s.","code":"rate_limit_exceeded"}}`),
			1*time.Second,
		)
	}
	return f.response, nil
}
func (f *flakyShortRateLimitProvider) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return "", nil
}

func TestManager_ShortRateLimitExceededRetriesSameProvider(t *testing.T) {
	t.Setenv("AI_REQUEST_DELAY_SECONDS", "0")
	m := metrics.New()

	p1 := &flakyShortRateLimitProvider{
		name: "groq",
		response: &ai.Response{
			Danish:        "Dansk ok",
			Ukrainian:     "Текст ок",
			TLDR:          "TLDR",
			Mood:          "neutral",
			AudienceScore: 6,
		},
	}
	p2 := &mockProvider{
		name: "openrouter",
		response: &ai.Response{
			Danish:        "Fallback",
			Ukrainian:     "Fallback",
			TLDR:          "TLDR",
			Mood:          "neutral",
			AudienceScore: 5,
		},
	}

	mgr := ai.NewManager(m, 5, p1, p2)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	resp, err := mgr.Generate(ctx, "T1", "C1", "sys", "user")
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.Danish != "Dansk ok" {
		t.Errorf("Expected p1 to succeed after short wait retry, got %q", resp.Danish)
	}
	if p1.calls != 2 {
		t.Errorf("Expected p1 to be called twice (initial + retry), got %d", p1.calls)
	}
	if p2.calls != 0 {
		t.Errorf("Expected p2 not to be called when p1 recovered, got %d", p2.calls)
	}
}

type sequenceMockProvider struct {
	name      string
	calls     int
	failAfter int
	response  *ai.Response
}

func (s *sequenceMockProvider) Name() string { return s.name }
func (s *sequenceMockProvider) Close()       {}
func (s *sequenceMockProvider) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	s.calls++
	if s.calls > s.failAfter {
		return nil, errors.New("503 service unavailable")
	}
	return s.response, nil
}
func (s *sequenceMockProvider) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return "", nil
}

func TestManager_PerProviderMaxRequestsAllowsFallback(t *testing.T) {
	t.Setenv("AI_REQUEST_DELAY_SECONDS", "0")
	m := metrics.New()

	p1 := &sequenceMockProvider{
		name:      "gemini",
		failAfter: 1,
		response: &ai.Response{
			Danish:        "Gemini Dansk",
			Ukrainian:     "Gemini Укр",
			TLDR:          "TLDR 1",
			Mood:          "neutral",
			AudienceScore: 7,
		},
	}
	p2 := &mockProvider{
		name: "groq",
		response: &ai.Response{
			Danish:        "Groq Dansk",
			Ukrainian:     "Groq Укр",
			TLDR:          "TLDR 2",
			Mood:          "neutral",
			AudienceScore: 7,
		},
	}

	// maxRequests=2 is tracked per provider:
	// Call 1: p1 succeeds (p1=1, p2=0)
	// Call 2: p1 fails -> fallback to p2 succeeds (p1=2, p2=1)
	// Call 3: p1 skipped (at limit 2), p2 serves it (p1=2, p2=2)
	// Call 4: both at limit 2 -> error
	mgr := ai.NewManager(m, 2, p1, p2)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	r1, err := mgr.Generate(ctx, "T1", "C1", "sys", "user")
	if err != nil || r1.Danish != "Gemini Dansk" {
		t.Fatalf("Call 1 expected Gemini Dansk, got resp=%+v err=%v", r1, err)
	}

	r2, err := mgr.Generate(ctx, "T2", "C2", "sys", "user")
	if err != nil || r2.Danish != "Groq Dansk" {
		t.Fatalf("Call 2 expected Groq Dansk, got resp=%+v err=%v", r2, err)
	}

	r3, err := mgr.Generate(ctx, "T3", "C3", "sys", "user")
	if err != nil || r3.Danish != "Groq Dansk" {
		t.Fatalf("Call 3 expected Groq Dansk while p1 at limit, got resp=%+v err=%v", r3, err)
	}
	if p1.calls != 2 {
		t.Errorf("Expected p1 to stop at limit 2, got %d calls", p1.calls)
	}
	if p2.calls != 2 {
		t.Errorf("Expected p2 to serve 2 calls, got %d calls", p2.calls)
	}

	if _, err := mgr.Generate(ctx, "T4", "C4", "sys", "user"); err == nil {
		t.Fatalf("Call 4 expected error when all providers reached limit, got nil")
	}
}
