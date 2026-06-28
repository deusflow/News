package digest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
)

type MockAIProvider struct {
	ResponseText string
}

func (m *MockAIProvider) Name() string {
	return "mock-ai"
}

func (m *MockAIProvider) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	return &ai.Response{
		Summary: m.ResponseText,
	}, nil
}

func (m *MockAIProvider) Close() {}

func TestRunDigest_FallbackToRSS(t *testing.T) {
	logger.Init()

	// 1. Создаем временную директорию для конфигов
	tmpDir, err := os.MkdirTemp("", "digest-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 2. Создаем временный feeds.yaml с одной тестовой лентой (например, RSS-лентой, которая вернет пустой список или ошибку, или мок)
	// Для тестов укажем несуществующий файл или валидный пустой файл, но лучше мок
	feedsPath := filepath.Join(tmpDir, "feeds.yaml")
	feedsContent := `
feeds:
  - url: "https://www.dr.dk/nyheder/service/feeds/allenyheder"
    name: "DR"
    lang: "da"
    priority: 1
    weight: 1
    active: false # Отключаем, чтобы не делать сетевых запросов в тестах
`
	if err := os.WriteFile(feedsPath, []byte(feedsContent), 0644); err != nil {
		t.Fatalf("failed to write feeds.yaml: %v", err)
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			UsePostgres: false,
		},
		RSS: config.RSSConfig{
			FeedsConfigPath: feedsPath,
		},
		Telegram: config.TelegramConfig{
			Token:  "mock-token",
			ChatID: "mock-chat",
		},
	}

	mockProv := &MockAIProvider{
		ResponseText: "✨ <b>ТИЖНЕВИЙ ДАЙДЖЕСТ</b>\n\n👉 <b>Тестова новина</b>\n💬 <i>Тестовий опис.</i>",
	}

	met := metrics.New()
	aiMgr := ai.NewManager(met, 0, mockProv)
	defer aiMgr.Close()

	// Запускаем дайджест. Он должен попытаться прочитать RSS, но так как активных фидов нет,
	// он вернет ошибку "all news sources failed or returned empty list".
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = RunDigest(ctx, cfg, aiMgr, nil)
	if err == nil {
		t.Errorf("expected error when no news sources are available, got nil")
	} else if !strings.Contains(err.Error(), "all news sources failed") {
		t.Errorf("expected 'all news sources failed' error, got: %v", err)
	}
}

func TestHasTextContent(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"", false},
		{"   ", false},
		{"<b></b>", false},
		{"<i> </i>", false},
		{"<b>\n</b>", false},
		{"&#8203;", false},
		{"&nbsp;", false},
		{"👉 <b>Заголовок</b>", true},
		{"Текст новини", true},
		{"<b>Новина</b> <i>суть</i>", true},
	}

	for _, tt := range tests {
		res := hasTextContent(tt.input)
		if res != tt.expected {
			t.Errorf("hasTextContent(%q) = %v; expected %v", tt.input, res, tt.expected)
		}
	}
}
