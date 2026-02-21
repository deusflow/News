package ai

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deusflow/News/internal/metrics"
)

type Manager struct {
	providers []Provider
	metrics   *metrics.Metrics

	// Очередь (Rate Limiter)
	mu           sync.Mutex
	lastCallTime time.Time
	delay        time.Duration
}

func NewManager(m *metrics.Metrics, providers ...Provider) *Manager {
	return &Manager{
		providers: providers,
		metrics:   m,
		// Устанавливаем строгую задержку: 1 запрос в 40 секунд
		delay: 40 * time.Second,
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

func tryParseRetrySeconds(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	lower := strings.ToLower(text)
	re := regexp.MustCompile(`(?i)retry(?:\s*(?:in|after))?\s*[:]?\s*(\d+(?:\.\d+)?)\s*s`)
	if m := re.FindStringSubmatch(lower); len(m) >= 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			return int(f + 0.5), true
		}
	}
	re2 := regexp.MustCompile(`(\d+)\s*seconds`)
	if match := re2.FindStringSubmatch(lower); len(match) >= 2 {
		if v, err := strconv.Atoi(match[1]); err == nil {
			return v, true
		}
	}
	return 0, false
}

// Generate работает как турникет: пускает строго по одному запросу с паузой 40 сек
func (m *Manager) Generate(ctx context.Context, title, content, prompt string) (*Response, error) {
	// Блокируем очередь для других потоков
	m.mu.Lock()

	timeSinceLastCall := time.Since(m.lastCallTime)

	// Если прошло меньше 40 секунд, ждем
	if timeSinceLastCall < m.delay {
		waitTime := m.delay - timeSinceLastCall
		log.Printf("⏳ Rate Limiter: Waiting %v before calling AI...", waitTime)

		select {
		case <-time.After(waitTime):
			// Время вышло, можно продолжать
		case <-ctx.Done():
			m.mu.Unlock() // Обязательно снимаем блокировку при отмене
			return nil, ctx.Err()
		}
	}

	m.lastCallTime = time.Now()

	// Отпускаем очередь, следующий поток начнет отсчитывать свои 40 секунд
	m.mu.Unlock()

	var lastErr error

	for _, provider := range m.providers {
		log.Printf("🤖 Trying AI provider: %s", provider.Name())

		resp, err := provider.Generate(ctx, title, content, prompt)
		if err == nil {
			log.Printf("✅ AI Success with %s", provider.Name())
			return resp, nil
		}

		if secs, ok := tryParseRetrySeconds(err.Error()); ok {
			if secs > 0 && secs <= 180 {
				log.Printf("⚠️ Provider %s requested retry after %ds — waiting...", provider.Name(), secs)
				if m.metrics != nil {
					m.metrics.IncrementAIRetry()
				}

				select {
				case <-time.After(time.Duration(secs+2) * time.Second):
				case <-ctx.Done():
					return nil, ctx.Err()
				}

				resp2, err2 := provider.Generate(ctx, title, content, prompt)
				if err2 == nil {
					log.Printf("✅ AI Success with %s (after wait)", provider.Name())
					return resp2, nil
				}
				lastErr = err2
				continue
			}
			lastErr = err
			continue
		}

		log.Printf("⚠️ Provider %s failed: %v", provider.Name(), err)
		lastErr = err
	}

	return nil, fmt.Errorf("all AI providers failed, last error: %v", lastErr)
}
