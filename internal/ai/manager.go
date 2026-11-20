package ai

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
)

// defaultDelay is the minimum pause between consecutive AI API calls.
//
// Gemini Free Tier limits (gemini-3.8-flash, as of 2026):
//   - 10 RPM  (requests per minute)  → minimum 6s between calls
//   - 250 RPD (requests per day)
//   - 1 000 000 TPM (tokens per minute)
//
// We use 7s as a safe default (≈8.5 RPM) so we never hit 10 RPM even
// with minor clock drift. Override with env AI_REQUEST_DELAY_SECONDS.
const defaultDelay = 7 * time.Second

// aiJob - структура задачи для очереди
type aiJob struct {
	ctx          context.Context
	title        string
	content      string
	systemPrompt string
	userPrompt   string
	rawMode      bool
	validate     ResponseValidator
	result       chan aiResult
}

// aiResult - структура ответа от ИИ
type aiResult struct {
	resp    *Response
	rawText string
	err     error
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

	jobQueue             chan aiJob
	cancelCtx            context.CancelFunc
	delay                time.Duration
	maxRequests          int
	requestCount         int
	providerRequestCount map[string]int
	lastCallByProvider   map[string]time.Time
	cooldownMu           sync.Mutex
	exhaustedUntil       map[string]time.Time
}

func NewManager(m *metrics.Metrics, maxRequests int, providers ...Provider) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	delay := defaultDelay
	if v := os.Getenv("AI_REQUEST_DELAY_SECONDS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			delay = time.Duration(secs) * time.Second
			logger.Info("AI request delay overridden", "delay", delay, "env_value", v)
		}
	}

	mgr := &Manager{
		providers:            providers,
		metrics:              m,
		jobQueue:             make(chan aiJob, 50), // буфер на 50 задач — достаточно для любого разумного батча
		cancelCtx:            cancel,
		delay:                delay,
		maxRequests:          maxRequests,
		providerRequestCount: make(map[string]int),
		lastCallByProvider:   make(map[string]time.Time),
		exhaustedUntil:       make(map[string]time.Time),
	}

	logger.Info("AI Manager started",
		"mode", "single-worker",
		"inter_request_delay", delay,
		"max_requests_per_provider", maxRequests)
	go mgr.worker(ctx)

	return mgr
}

func (m *Manager) isCooldown(provider string) bool {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	if until, ok := m.exhaustedUntil[provider]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(m.exhaustedUntil, provider)
	}
	return false
}

func (m *Manager) markCooldown(provider string, duration time.Duration) {
	if duration <= 0 {
		duration = 5 * time.Minute
	}
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	m.exhaustedUntil[provider] = time.Now().Add(duration)
	logger.Warn("AI provider marked on cooldown", "provider", provider, "cooldown", duration)
}

func extractRetryAfterSeconds(err error) int {
	if err == nil {
		return 0
	}
	if perr, ok := AsProviderError(err); ok && perr.Kind == ErrorKindRateLimited && perr.RetryAfter > 0 {
		return int((perr.RetryAfter + 500*time.Millisecond) / time.Second)
	}
	if secs, ok := ParseRetryAfterSeconds(err.Error()); ok {
		return secs
	}
	return 0
}

func isExhaustionError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "exhausted") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "daily limit") ||
		strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "tokens per day") ||
		strings.Contains(lower, "requests per day") ||
		strings.Contains(lower, "per-day") ||
		strings.Contains(lower, "tpd") ||
		strings.Contains(lower, "rpd") {
		return true
	}
	if strings.Contains(lower, "rate_limit_exceeded") {
		// Short TPM windows include a Retry-After duration; wait/cooldown cleanly instead of 5m ban.
		return extractRetryAfterSeconds(err) <= 0
	}
	if perr, ok := AsProviderError(err); ok && perr.Kind == ErrorKindRateLimited && perr.RetryAfter >= 5*time.Minute {
		return true
	}
	return false
}

func (m *Manager) providerLimitReached(providerName string) bool {
	return m.maxRequests > 0 && m.providerRequestCount[providerName] >= m.maxRequests
}

func (m *Manager) recordProviderCall(providerName string) {
	m.requestCount++
	m.providerRequestCount[providerName]++
	m.lastCallByProvider[providerName] = time.Now()
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

// providerDelay returns the minimum inter-request delay for a given provider.
// Gemini Free Tier is capped at 10 RPM (default 7-8s), while Groq/OpenRouter/Cerebras
// allow 20-30 RPM so they don't need an 8s sleep between requests.
func (m *Manager) providerDelay(providerName string) time.Duration {
	if m.delay <= 0 {
		return 0
	}
	lower := strings.ToLower(providerName)
	if strings.HasPrefix(lower, "groq") ||
		strings.HasPrefix(lower, "openrouter") ||
		strings.HasPrefix(lower, "cerebras") {
		if m.delay > 2*time.Second {
			return 2 * time.Second
		}
	}
	return m.delay
}

// waitForProvider enforces the per-provider rate-limit delay before calling providerName.
func (m *Manager) waitForProvider(ctx context.Context, providerName string) error {
	delay := m.providerDelay(providerName)
	if delay <= 0 {
		return nil
	}
	lastCall, ok := m.lastCallByProvider[providerName]
	if !ok || lastCall.IsZero() {
		return nil
	}
	elapsed := time.Since(lastCall)
	if elapsed >= delay {
		return nil
	}

	wait := delay - elapsed
	logger.Info("AI queue: enforcing provider delay",
		"provider", providerName,
		"wait", wait.Round(time.Millisecond),
		"elapsed_sec", fmt.Sprintf("%.1f", elapsed.Seconds()))

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// worker обрабатывает задачи строго последовательно (одна за одной).
// Это принципиально: Gemini Free Tier не терпит параллельных запросов.
func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case job := <-m.jobQueue:
			// Если вызывающий уже отказался от задачи — пропускаем без задержки.
			if job.ctx.Err() != nil {
				job.result <- aiResult{nil, "", job.ctx.Err()}
				continue
			}

			// Связываем контекст задачи с общим контекстом воркера, чтобы не зависнуть при Close().
			jobCtx, cancelJob := context.WithCancel(job.ctx)
			stopWatch := context.AfterFunc(ctx, cancelJob)

			start := time.Now()

			var resp *Response
			var rawText string
			var err error

			if job.rawMode {
				rawText, err = m.executeRawWithFallback(jobCtx, job.systemPrompt, job.userPrompt)
			} else {
				resp, err = m.executeWithFallback(jobCtx, job.title, job.content, job.systemPrompt, job.userPrompt, job.validate)
			}

			stopWatch()
			cancelJob()

			if ctx.Err() != nil && job.ctx.Err() == nil {
				return
			}

			if err == nil {
				logger.Info("AI request done",
					"duration_sec", fmt.Sprintf("%.1f", time.Since(start).Seconds()),
					"raw_mode", job.rawMode)
			}

			job.result <- aiResult{resp: resp, rawText: rawText, err: err}
		}
	}
}

// executeWithFallback пробует каждого провайдера по очереди.
// Если провайдер вернул 429 с Retry-After — ждём ровно столько, сколько просят,
// и повторяем этого же провайдера один раз перед переходом к следующему.
// Если передан validate и ответ не проходит проверку качества (например, брак украинского
// или галлюцинация числа), провайдер не банится, а выполняется автоматический фоллбэк на следующий.
func (m *Manager) executeWithFallback(ctx context.Context, title, content, systemPrompt, userPrompt string, validate ResponseValidator) (*Response, error) {
	var lastErr error

	for _, provider := range m.providers {
		if m.isCooldown(provider.Name()) {
			logger.Info("AI provider on cooldown, skipping", "provider", provider.Name())
			continue
		}

		if m.providerLimitReached(provider.Name()) {
			logger.Warn("AI provider request limit reached, skipping", "provider", provider.Name(), "limit", m.maxRequests)
			lastErr = fmt.Errorf("AI provider %s hard limit reached: %d requests per run", provider.Name(), m.maxRequests)
			continue
		}

		if err := m.waitForProvider(ctx, provider.Name()); err != nil {
			return nil, err
		}

		logger.Info("trying AI provider", "provider", provider.Name())

		m.recordProviderCall(provider.Name())
		rsp, err := provider.Generate(ctx, title, content, systemPrompt, userPrompt)
		m.lastCallByProvider[provider.Name()] = time.Now()
		if err == nil {
			if validate != nil {
				if valErr := validate(rsp); valErr != nil {
					logger.Warn("AI response rejected by quality validator, trying next provider",
						"provider", provider.Name(),
						"title", title,
						"error", valErr)
					lastErr = valErr
					continue
				}
			}
			logger.Info("AI success", "provider", provider.Name(), "ai_request_count", m.requestCount, "provider_request_count", m.providerRequestCount[provider.Name()])
			return rsp, nil
		}

		if isExhaustionError(err) {
			m.markCooldown(provider.Name(), 5*time.Minute)
			logger.Warn("AI provider exhausted, falling back", "provider", provider.Name(), "error", err)
			lastErr = err
			continue
		}

		retryAfter := extractRetryAfterSeconds(err)
		if retryAfter > 20 {
			cooldown := time.Duration(retryAfter+2) * time.Second
			m.markCooldown(provider.Name(), cooldown)
			logger.Warn("AI provider rate limited for long period, marking cooldown",
				"provider", provider.Name(),
				"wait_sec", retryAfter,
				"cooldown", cooldown)
			lastErr = err
			continue
		}

		if retryAfter > 0 && retryAfter <= 20 {
			logger.Warn("AI provider rate limited",
				"provider", provider.Name(),
				"wait_sec", retryAfter)
			if m.metrics != nil {
				m.metrics.IncrementAIRetry()
			}

			timer := time.NewTimer(time.Duration(retryAfter+2) * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}

			if m.providerLimitReached(provider.Name()) {
				logger.Warn("AI provider request limit reached before retry, falling back", "provider", provider.Name(), "limit", m.maxRequests)
				lastErr = fmt.Errorf("AI provider %s hard limit reached during retry: %d requests per run", provider.Name(), m.maxRequests)
				continue
			}

			m.recordProviderCall(provider.Name())
			resp2, err2 := provider.Generate(ctx, title, content, systemPrompt, userPrompt)
			m.lastCallByProvider[provider.Name()] = time.Now()
			if err2 == nil {
				if validate != nil {
					if valErr := validate(resp2); valErr != nil {
						logger.Warn("AI response rejected by quality validator after rate-limit wait, trying next provider",
							"provider", provider.Name(),
							"title", title,
							"error", valErr)
						lastErr = valErr
						continue
					}
				}
				logger.Info("AI success after rate-limit wait", "provider", provider.Name(), "ai_request_count", m.requestCount, "provider_request_count", m.providerRequestCount[provider.Name()])
				return resp2, nil
			}
			m.markCooldown(provider.Name(), 5*time.Minute)
			lastErr = err2
			continue
		}

		logger.Warn("AI provider failed", "provider", provider.Name(), "error", err)
		lastErr = err
	}

	return nil, fmt.Errorf("all AI providers failed, last error: %v", lastErr)
}

// executeRawWithFallback tries each provider in turn for a raw text response.
func (m *Manager) executeRawWithFallback(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	var lastErr error

	for _, provider := range m.providers {
		if m.isCooldown(provider.Name()) {
			logger.Info("AI provider on cooldown, skipping", "provider", provider.Name())
			continue
		}

		if m.providerLimitReached(provider.Name()) {
			logger.Warn("AI provider request limit reached, skipping raw", "provider", provider.Name(), "limit", m.maxRequests)
			lastErr = fmt.Errorf("AI provider %s hard limit reached: %d requests per run", provider.Name(), m.maxRequests)
			continue
		}

		if err := m.waitForProvider(ctx, provider.Name()); err != nil {
			return "", err
		}

		m.recordProviderCall(provider.Name())
		logger.Debug("AI Request Raw", "provider", provider.Name())

		rawText, err := provider.GenerateRaw(ctx, systemPrompt, userPrompt)
		m.lastCallByProvider[provider.Name()] = time.Now()
		if err != nil {
			logger.Error("Provider Raw failed", "provider", provider.Name(), "error", err)

			if isExhaustionError(err) {
				m.markCooldown(provider.Name(), 5*time.Minute)
				lastErr = err
				continue
			}

			retryAfter := extractRetryAfterSeconds(err)
			if retryAfter > 20 {
				cooldown := time.Duration(retryAfter+2) * time.Second
				m.markCooldown(provider.Name(), cooldown)
				lastErr = err
				continue
			}

			if retryAfter > 0 && retryAfter <= 20 {
				logger.Warn("Rate limited Raw", "provider", provider.Name(), "wait_sec", retryAfter)
				timer := time.NewTimer(time.Duration(retryAfter+2) * time.Second)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return "", ctx.Err()
				}

				if m.providerLimitReached(provider.Name()) {
					lastErr = fmt.Errorf("AI provider %s hard limit reached during raw retry: %d requests per run", provider.Name(), m.maxRequests)
					continue
				}

				m.recordProviderCall(provider.Name())
				rawText2, retryErr := provider.GenerateRaw(ctx, systemPrompt, userPrompt)
				m.lastCallByProvider[provider.Name()] = time.Now()
				if retryErr == nil {
					return rawText2, nil
				}
				m.markCooldown(provider.Name(), 5*time.Minute)
				lastErr = retryErr
				continue
			}

			lastErr = err
			continue
		}

		return rawText, nil
	}

	if lastErr != nil {
		return "", fmt.Errorf("all providers failed. last error: %w", lastErr)
	}
	return "", fmt.Errorf("no AI providers available")
}

// Generate requests a standard JSON-parsed AI response.
func (m *Manager) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*Response, error) {
	return m.GenerateValidated(ctx, title, content, systemPrompt, userPrompt, nil)
}

// GenerateValidated requests a JSON-parsed AI response and automatically falls back to the next
// provider if validate returns an error (e.g. broken Ukrainian or hallucinated numbers).
func (m *Manager) GenerateValidated(ctx context.Context, title, content, systemPrompt, userPrompt string, validate ResponseValidator) (*Response, error) {
	resultCh := make(chan aiResult, 1)

	job := aiJob{
		ctx:          ctx,
		title:        title,
		content:      content,
		systemPrompt: systemPrompt,
		userPrompt:   userPrompt,
		rawMode:      false,
		validate:     validate,
		result:       resultCh,
	}

	select {
	case m.jobQueue <- job:
		res := <-resultCh
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GenerateRaw requests a pure string response from the AI.
func (m *Manager) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resultCh := make(chan aiResult, 1)

	job := aiJob{
		ctx:          ctx,
		systemPrompt: systemPrompt,
		userPrompt:   userPrompt,
		rawMode:      true,
		result:       resultCh,
	}

	select {
	case m.jobQueue <- job:
		res := <-resultCh
		return res.rawText, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
