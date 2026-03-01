package ai

import (
	"context"
	"fmt"
	"strings"
)

// Response - единый формат ответа от любого AI
type Response struct {
	Summary        string   `json:"summary"`
	Danish         string   `json:"danish"`
	Ukrainian      string   `json:"ukrainian"`
	TitleUkrainian string   `json:"title_ukrainian"`
	Mood           string   `json:"mood"`
	Category       string   `json:"category"` // AI выбирает из фиксированного списка в prompt
	Tags           []string `json:"tags"`
	TLDR           string   `json:"tldr"`
	FunFact        string   `json:"fun_fact"`
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

	return nil
}

// Provider - интерфейс, который должны реализовать все модели
type Provider interface {
	Name() string
	Generate(ctx context.Context, title, content, prompt string) (*Response, error)
	Close()
}
