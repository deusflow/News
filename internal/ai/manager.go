package ai

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deusflow/News/internal/metrics"
)

// aiJob - структура задачи для очереди
type aiJob struct {
	ctx     context.Context
	title   string
	content string
	prompt  string
	result  chan aiResult
}

// aiResult - структура ответа от ИИ
type aiResult struct {
	resp *Response
	err  error
}

type Manager struct {
	providers []Provider
	metrics   *metrics.Metrics

	jobQueue  chan aiJob         // Канал для задач (Queue)
	cancelCtx context.CancelFunc // Функция для остановки воркера
	delay     time.Duration
}

func NewManager(m *metrics.Metrics, providers ...Provider) *Manager {
	// Создаем контекст для управления жизненным циклом воркера
	ctx, cancel := context.WithCancel(context.Background())

	mgr := &Manager{
		providers: providers,
		metrics:   m,
		jobQueue:  make(chan aiJob, 50), // Bounded queue (лимит в 50 задач одновременно)
		cancelCtx: cancel,
		delay:     40 * time.Second, // 1 запрос в 40 секунд
	}

	// Запускаем фонового воркера (Менеджера очереди)
	go mgr.worker(ctx)

	return mgr
}

func (m *Manager) Close() {
	m.cancelCtx() // Останавливаем фонового воркера
	for _, p := range m.providers {
		p.Close()
	}
}

func (m *Manager) Name() string {
	return "ai-manager"
}

// worker обрабатывает задачи строго по одной с заданным интервалом
func (m *Manager) worker(ctx context.Context) {
	var lastCall time.Time

	for {
		select {
		case <-ctx.Done():
			return // Завершаем работу воркера при выключении программы

		case job := <-m.jobQueue:
			// Если задача уже была отменена (например, вышел таймаут),
			// сразу возвращаем ошибку и берем следующую без задержек.
			if job.ctx.Err() != nil {
				job.result <- aiResult{nil, job.ctx.Err()}
				continue
			}

			// Вычисляем, нужно ли подождать
			elapsed := time.Since(lastCall)
			if elapsed < m.delay {
				wait := m.delay - elapsed
				log.Printf("⏳ Worker queue: waiting %v before calling AI...", wait)

				// Используем безопасный таймер вместо time.After, чтобы избежать утечек памяти
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-job.ctx.Done(): // Если отменили во время ожидания
					timer.Stop()
					job.result <- aiResult{nil, job.ctx.Err()}
					continue
				case <-timer.C:
					// Ожидание окончено
				}
			}

			// Запускаем обработку через нейросети
			resp, err := m.executeWithFallback(job.ctx, job.title, job.content, job.prompt)

			lastCall = time.Now()
			job.result <- aiResult{resp, err}
		}
	}
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

// executeWithFallback пытается выполнить запрос, переключаясь между ИИ при ошибке
func (m *Manager) executeWithFallback(ctx context.Context, title, content, prompt string) (*Response, error) {
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

				timer := time.NewTimer(time.Duration(secs+2) * time.Second)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
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

// Generate теперь просто отправляет задачу в канал (Queue) и ждет ответа
func (m *Manager) Generate(ctx context.Context, title, content, prompt string) (*Response, error) {
	resultCh := make(chan aiResult, 1)

	job := aiJob{
		ctx:     ctx,
		title:   title,
		content: content,
		prompt:  prompt,
		result:  resultCh,
	}

	// Отправляем задачу в очередь
	select {
	case m.jobQueue <- job:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Ждем результат из очереди
	select {
	case res := <-resultCh:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
