package metrics

import (
	"sync"
	"time"
)

type Metrics struct {
	mu sync.RWMutex

	// Counters
	TotalNewsProcessed     int64
	SuccessfulTranslations int64
	FailedTranslations     int64
	DuplicatesFiltered     int64
	TelegramMessagesSent   int64

	// Timings
	LastProcessingTime    time.Duration
	AverageProcessingTime time.Duration
	TotalProcessingTime   time.Duration
	ProcessingCount       int64

	// Status
	LastRunTime   time.Time
	LastErrorTime time.Time
	LastError     string
	IsHealthy     bool
}

var Global = &Metrics{IsHealthy: true}

func (m *Metrics) IncrementNewsProcessed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalNewsProcessed++
}

func (m *Metrics) IncrementSuccessfulTranslations() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SuccessfulTranslations++
}

func (m *Metrics) IncrementFailedTranslations() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailedTranslations++
}

func (m *Metrics) IncrementDuplicatesFiltered() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DuplicatesFiltered++
}

func (m *Metrics) IncrementTelegramMessagesSent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TelegramMessagesSent++
}

func (m *Metrics) RecordProcessingTime(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.LastProcessingTime = duration
	m.TotalProcessingTime += duration
	m.ProcessingCount++

	if m.ProcessingCount > 0 {
		m.AverageProcessingTime = m.TotalProcessingTime / time.Duration(m.ProcessingCount)
	}
}

func (m *Metrics) SetLastRun() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LastRunTime = time.Now()
	m.IsHealthy = true
}

func (m *Metrics) SetError(err string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LastError = err
	m.LastErrorTime = time.Now()
	m.IsHealthy = false
}

func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]interface{}{
		"total_news_processed":       m.TotalNewsProcessed,
		"successful_translations":    m.SuccessfulTranslations,
		"failed_translations":        m.FailedTranslations,
		"duplicates_filtered":        m.DuplicatesFiltered,
		"telegram_messages_sent":     m.TelegramMessagesSent,
		"last_processing_time_ms":    m.LastProcessingTime.Milliseconds(),
		"average_processing_time_ms": m.AverageProcessingTime.Milliseconds(),
		"last_run_time":              m.LastRunTime.Format(time.RFC3339),
		"last_error_time":            m.LastErrorTime.Format(time.RFC3339),
		"last_error":                 m.LastError,
		"is_healthy":                 m.IsHealthy,
	}
}
