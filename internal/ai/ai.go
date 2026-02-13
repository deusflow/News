package ai

import (
	"context"
)

// Response - единый формат ответа от любого AI
type Response struct {
	Summary        string   `json:"summary"`
	Danish         string   `json:"danish"`
	Ukrainian      string   `json:"ukrainian"`
	TitleUkrainian string   `json:"title_ukrainian"`
	Mood           string   `json:"mood"`
	Tags           []string `json:"tags"`
	TLDR           string   `json:"tldr"`
	FunFact        string   `json:"fun_fact"`
}

// Provider - интерфейс, который должны реализовать все модели
type Provider interface {
	Name() string
	Generate(ctx context.Context, title, content, prompt string) (*Response, error)
	Close()
}
