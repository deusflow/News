package ai

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
)

// defaultDelay is the minimum pause between consecutive AI API calls.
//
// Gemini Free Tier limits (gemini-2.5-flash, as of 2026):
//   - 10 RPM  (requests per minute)  → minimum 6s between calls
//   - 250 RPD (requests per day)
//   - 1 000 000 TPM (tokens per minute)
//
// We use 7s as a safe default (≈8.5 RPM) so we never hit 10 RPM even
// with minor clock drift. Override with env AI_REQUEST_DELAY_SECONDS.
const defaultDelay = 7 * time.Second

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

// Manager serialises all AI calls through a single background worker.
//
// Design rationale:
//   - Gemini Free Tier allows only 10 RPM. Sending requests in parallel
//     would immediately exhaust the quota and trigger 429 errors.
//   - A single worker with a configurable inter-request delay is the
//     simplest and most reliable way to stay within the quota.
//   - Rate limiting lives HERE, not inside individual provider clients.
//     Each provider's Generate() just calls the API and returns.
type Manager struct {
	providers []Provider
	metrics   *metrics.Metrics

	jobQueue  chan aiJob
	cancelCtx context.CancelFunc
	delay     time.Duration
}

func NewManager(m *metrics.Metrics, providers ...Provider) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	delay := defaultDelay
	if v := os.Getenv("AI_REQUEST_DELAY_SECONDS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			delay = time.Duration(secs) * time.Second
			logger.Info("AI request delay overridden", "delay", delay, "env_value", v)
		}
	}

	mgr := &Manager{
		providers: providers,
		metrics:   m,
		jobQueue:  make(chan aiJob, 50), // буфер на 50 задач — достаточно для любого разумного батча
		cancelCtx: cancel,
		delay:     delay,
	}

	logger.Info("AI Manager started", "mode", "single-worker", "inter_request_delay", delay)
	go mgr.worker(ctx)

	return mgr
}

func (m *Manager) Close() {
	m.cancelCtx()
	for _, p := range m.providers {
		p.Close()
	}
}

func (m *Manager) Name() string {
	return "ai-manager"
}

// worker обрабатывает задачи строго последовательно (одна за одной).
// Это принципиально: Gemini Free Tier не терпит параллельных запросов.
func (m *Manager) worker(ctx context.Context) {
	var lastCall time.Time

	for {
		select {
		case <-ctx.Done():
			return

		case job := <-m.jobQueue:
			// Если вызывающий уже отказался от задачи — пропускаем без задержки.
			if job.ctx.Err() != nil {
				job.result <- aiResult{nil, job.ctx.Err()}
				continue
			}

			// Соблюдаем паузу между запросами.
			elapsed := time.Since(lastCall)
			if !lastCall.IsZero() && elapsed < m.delay {
				wait := m.delay - elapsed
				logger.Info("AI queue: enforcing delay",
					"wait", wait.Round(time.Millisecond),
					"elapsed_sec", fmt.Sprintf("%.1f", elapsed.Seconds()))

				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-job.ctx.Done():
					timer.Stop()
					job.result <- aiResult{nil, job.ctx.Err()}
					continue
				case <-timer.C:
					// пауза выдержана
				}
			}

			start := time.Now()
			resp, err := m.executeWithFallback(job.ctx, job.title, job.content, job.prompt)
			lastCall = time.Now()

			if err == nil {
				logger.Info("AI request done",
					"duration_sec", fmt.Sprintf("%.1f", time.Since(start).Seconds()),
					"next_allowed_in", m.delay)
			}

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

// executeWithFallback пробует каждого провайдера по очереди.
// Если провайдер вернул 429 с Retry-After — ждём ровно столько, сколько просят,
// и повторяем этого же провайдера один раз перед переходом к следующему.
func (m *Manager) executeWithFallback(ctx context.Context, title, content, prompt string) (*Response, error) {
	var lastErr error

	for _, provider := range m.providers {
		logger.Info("trying AI provider", "provider", provider.Name())

		resp, err := provider.Generate(ctx, title, content, prompt)
		if err == nil {
			logger.Info("AI success", "provider", provider.Name())
			return resp, nil
		}

		if secs, ok := tryParseRetrySeconds(err.Error()); ok && secs > 0 && secs <= 180 {
			logger.Warn("AI provider rate limited",
				"provider", provider.Name(),
				"wait_sec", secs)
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
				logger.Info("AI success after rate-limit wait", "provider", provider.Name())
				return resp2, nil
			}
			lastErr = err2
			continue
		}

		logger.Warn("AI provider failed", "provider", provider.Name(), "error", err)
		lastErr = err
	}

	return nil, fmt.Errorf("all AI providers failed, last error: %v", lastErr)
}

// Generate ставит задачу в очередь и блокируется до получения результата.
// Вызывается из горутин worker-pool в news.go — все они сериализуются через
// единственного воркера Manager'а, что и обеспечивает соблюдение RPM-лимита.
func (m *Manager) Generate(ctx context.Context, title, content, prompt string) (*Response, error) {
	resultCh := make(chan aiResult, 1)

	job := aiJob{
		ctx:     ctx,
		title:   title,
		content: content,
		prompt:  prompt,
		result:  resultCh,
	}

	select {
	case m.jobQueue <- job:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case res := <-resultCh:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
