package ai

import (
	"context"
	"fmt"
	"log"
)

type Manager struct {
	providers []Provider
}

func NewManager(providers ...Provider) *Manager {
	return &Manager{
		providers: providers,
	}
}

func (m *Manager) Close() {
	for _, p := range m.providers {
		p.Close()
	}
}

func (m *Manager) Name() string {
	return "ai-manager"
}

// Generate пробует провайдеров по очереди
func (m *Manager) Generate(ctx context.Context, title, content, prompt string) (*Response, error) {
	var lastErr error

	for _, provider := range m.providers {
		log.Printf("🤖 Trying AI provider: %s", provider.Name())

		resp, err := provider.Generate(ctx, title, content, prompt)
		if err == nil {
			log.Printf("✅ AI Success with %s", provider.Name())
			return resp, nil
		}

		log.Printf("⚠️ Provider %s failed: %v", provider.Name(), err)
		lastErr = err
	}

	return nil, fmt.Errorf("all AI providers failed, last error: %v", lastErr)
}
