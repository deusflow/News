package ai

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Response - единый формат ответа от любого AI
type Response struct {
	Summary        string   `json:"summary"`
	Danish         string   `json:"danish"`
	Ukrainian      string   `json:"ukrainian"`
	TitleDanish    string   `json:"title_danish"`
	TitleUkrainian string   `json:"title_ukrainian"`
	Mood           string   `json:"mood"`
	Category       string   `json:"category"` // AI выбирает из фиксированного списка в prompt
	Tags           []string `json:"tags"`
	TLDR           string   `json:"tldr"`
	FunFact        string   `json:"fun_fact"`
	WhyItMatters   string   `json:"why_it_matters"`
	IsExclusive    bool     `json:"is_exclusive"`
}

// validMoods — whitelist допустимых значений mood.
var validMoods = map[string]bool{
	"positive": true,
	"negative": true,
	"neutral":  true,
	"shocking": true,
	"urgent":   true,
}

// Validate проверяет обязательные поля ответа AI и нормализует значения.
// Если обязательное поле пустое — возвращает ошибку (новость будет пропущена).
// Если mood невалидный — тихо заменяет на "neutral" вместо падения.
func (r *Response) Validate() error {
	if strings.TrimSpace(r.Danish) == "" {
		return fmt.Errorf("AI returned empty danish text")
	}
	if strings.TrimSpace(r.Ukrainian) == "" {
		return fmt.Errorf("AI returned empty ukrainian text")
	}
	if strings.TrimSpace(r.TLDR) == "" {
		return fmt.Errorf("AI returned empty tldr")
	}

	// Нормализуем mood: приводим к lowercase, при неизвестном значении — fallback
	r.Mood = strings.ToLower(strings.TrimSpace(r.Mood))
	if !validMoods[r.Mood] {
		r.Mood = "neutral"
	}

	// category нормализуем (валидация через news.ValidateCategory происходит в news.go)
	r.Category = strings.ToLower(strings.TrimSpace(r.Category))

	// Редакторский вывод обязателен для поста; если модель его пропустила,
	// используем TLDR как безопасный fallback вместо пропуска новости.
	r.WhyItMatters = strings.TrimSpace(r.WhyItMatters)
	if r.WhyItMatters == "" {
		r.WhyItMatters = strings.TrimSpace(r.TLDR)
	}

	return nil
}

// Provider - интерфейс, который должны реализовать все модели
type Provider interface {
	Name() string
	Generate(ctx context.Context, title, content, prompt string) (*Response, error)
	Close()
}

// ErrorKind classifies provider failures for resilient fallback decisions.
type ErrorKind string

const (
	ErrorKindUnknown     ErrorKind = "unknown"
	ErrorKindRateLimited ErrorKind = "rate_limited"
	ErrorKindTemporary   ErrorKind = "temporary"
	ErrorKindPermanent   ErrorKind = "permanent"
)

// ProviderError carries structured metadata about AI provider failures.
type ProviderError struct {
	Provider   string
	Kind       ErrorKind
	RetryAfter time.Duration
	Err        error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "provider error"
	}
	if e.Provider == "" {
		return fmt.Sprintf("provider %s", e.Kind)
	}
	if e.Err == nil {
		return fmt.Sprintf("provider=%s kind=%s", e.Provider, e.Kind)
	}
	return fmt.Sprintf("provider=%s kind=%s: %v", e.Provider, e.Kind, e.Err)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ParseRetryAfterSeconds extracts retry delay from mixed provider error texts.
func ParseRetryAfterSeconds(text string) (int, bool) {
	if strings.TrimSpace(text) == "" {
		return 0, false
	}
	lower := strings.ToLower(text)

	re := regexp.MustCompile(`(?i)retry(?:\s*(?:in|after))?\s*[:=]?\s*(\d+(?:\.\d+)?)\s*s`)
	if m := re.FindStringSubmatch(lower); len(m) >= 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			return int(f + 0.5), true
		}
	}

	re2 := regexp.MustCompile(`(?i)(\d+)\s*seconds`)
	if m := re2.FindStringSubmatch(lower); len(m) >= 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v, true
		}
	}

	// Gemini errors often include retry_delay:"6s".
	re3 := regexp.MustCompile(`(?i)retry[_\s-]*delay\s*[:=]\s*"?(\d+)s"?`)
	if m := re3.FindStringSubmatch(lower); len(m) >= 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v, true
		}
	}

	return 0, false
}

// NewRateLimitedError wraps provider errors with typed rate-limit metadata.
func NewRateLimitedError(provider string, err error, retryAfter time.Duration) error {
	return &ProviderError{
		Provider:   provider,
		Kind:       ErrorKindRateLimited,
		RetryAfter: retryAfter,
		Err:        err,
	}
}

// AsProviderError unwraps a ProviderError if present.
func AsProviderError(err error) (*ProviderError, bool) {
	if err == nil {
		return nil, false
	}
	var perr *ProviderError
	if ok := errors.As(err, &perr); ok && perr != nil {
		return perr, true
	}
	return nil, false
}
