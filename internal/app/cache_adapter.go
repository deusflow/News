package app

import (
	"context"
	"time"

	"github.com/deusflow/News/internal/storage"
)

// DedupeStore handles deduplication queries, hashing, and marking articles as sent.
type DedupeStore interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsSourceURLSent(sourceURL string) bool
	IsContentDuplicate(content string) (bool, string)
	IsTitleNearDuplicate(title string) (bool, string)
	CheckSemanticDuplicate(clusterKey string, candidateEmbedding []float32, titleUA string, lookback time.Duration, keyThreshold, cosineThreshold float64, shadowMode bool) (storage.SemanticCheckResult, error)
	GetRecentStories(lookback time.Duration) ([]storage.SentStoryRecord, error)
	SaveTelegramMessageID(hash string, msgID int) error
	MarkAsSent(hash, title, link, category, source string) error
	MarkAsSentWithContent(hash, title, link, content, category, source string) error
	MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey string, emb []float32) error
	TryReservePublish(hash, title, url string) (bool, error)
	CancelPublishReservation(hash string) error
	IsFunFactRecentlyUsed(funFact string) bool
	MarkFunFactUsed(funFact string) error
}

// StateStore persists key-value status for health monitoring and run counters.
type StateStore interface {
	GetState(key string) (string, error)
	SetState(key, value string) error
}

// DLQStore manages the dead letter queue for failed Telegram deliveries.
type DLQStore interface {
	SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error
	GetFailedNews(limit int) ([]storage.FailedItem, error)
	DeleteFailedNews(id int) error
	IncrementFailedAttempts(id int, errorMsg string) error
}

// DelayedPostStore manages delayed high-impact publications.
type DelayedPostStore interface {
	EnqueueDelayedPost(hash string, title, link, newsJSON string, delay time.Duration) error
	GetReadyDelayedPosts(ctx context.Context) ([]storage.DelayedPost, error)
	MarkDelayedPostSent(id int) error
	MarkDelayedPostFailed(id int, errMsg string) error
}

// SyncQueueStore manages reliable secondary delivery to Supabase.
type SyncQueueStore interface {
	EnqueueSupabaseSync(hash string, payload []byte) error
	GetPendingSupabaseSync(limit int) ([]storage.SyncQueueItem, error)
	DeleteSyncQueueItem(id int) error
	IncrementSyncQueueAttempts(id int, errMsg string) error
	MarkSupabaseSynced(hash string) error
}

// AITranslationCache caches LLM translation payloads to save token costs and prevent duplicate API calls.
type AITranslationCache interface {
	GetCachedAITranslation(hash string) (string, error)
	SaveCachedAITranslation(hash, link, title, responseJSON string) error
}

// CacheAdapter is the aggregate contract embedding all storage capabilities.
type CacheAdapter interface {
	DedupeStore
	StateStore
	DLQStore
	DelayedPostStore
	SyncQueueStore
	AITranslationCache
}

// FileCacheAdapter — Ленивый партнер (Файловый кэш).
// Он подписывает контракт, но для новых методов просто говорит "Ок" и ничего не делает.
type FileCacheAdapter struct {
	cache *storage.FileCache
}

func (f *FileCacheAdapter) GenerateNewsHash(title, link string) string {
	return f.cache.GenerateNewsHash(title, link)
}

func (f *FileCacheAdapter) IsAlreadySent(hash string) bool {
	return f.cache.IsAlreadySent(hash)
}

func (f *FileCacheAdapter) IsSourceURLSent(sourceURL string) bool {
	return false
}

func (f *FileCacheAdapter) IsContentDuplicate(content string) (bool, string) {
	return false, ""
}

func (f *FileCacheAdapter) IsTitleNearDuplicate(title string) (bool, string) {
	return false, ""
}

func (f *FileCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	f.cache.MarkAsSent(hash, title, link, category, source)
	return nil
}

func (f *FileCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	f.cache.MarkAsSent(hash, title, link, category, source)
	return nil
}

func (f *FileCacheAdapter) MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey string, emb []float32) error {
	f.cache.MarkAsSentWithSemanticData(hash, title, link, category, source, titleUA, clusterKey, emb)
	return nil
}

func (f *FileCacheAdapter) CheckSemanticDuplicate(clusterKey string, candidateEmbedding []float32, titleUA string, lookback time.Duration, keyThreshold, cosineThreshold float64, shadowMode bool) (storage.SemanticCheckResult, error) {
	return f.cache.CheckSemanticDuplicate(clusterKey, candidateEmbedding, titleUA, lookback, keyThreshold, cosineThreshold, shadowMode)
}

func (f *FileCacheAdapter) GetRecentStories(lookback time.Duration) ([]storage.SentStoryRecord, error) {
	return f.cache.GetRecentStories(lookback)
}

func (f *FileCacheAdapter) SaveTelegramMessageID(hash string, msgID int) error {
	return f.cache.SaveTelegramMessageID(hash, msgID)
}

// --- SYNC QUEUE STUBS (file cache has no Supabase sync queue) ---

func (f *FileCacheAdapter) MarkSupabaseSynced(hash string) error {
	return nil
}

func (f *FileCacheAdapter) IsFunFactRecentlyUsed(funFact string) bool {
	return false
}

func (f *FileCacheAdapter) MarkFunFactUsed(funFact string) error {
	return nil
}

func (f *FileCacheAdapter) EnqueueSupabaseSync(hash string, payload []byte) error {
	return nil
}

func (f *FileCacheAdapter) GetPendingSupabaseSync(limit int) ([]storage.SyncQueueItem, error) {
	return nil, nil
}

func (f *FileCacheAdapter) DeleteSyncQueueItem(id int) error {
	return nil
}

func (f *FileCacheAdapter) IncrementSyncQueueAttempts(id int, errMsg string) error {
	return nil
}

// --- ЗАГЛУШКИ (STUBS) ---
// Чтобы Go не ругался, что FileCacheAdapter не соответствует интерфейсу.

func (f *FileCacheAdapter) SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error {
	return nil
}

func (f *FileCacheAdapter) GetFailedNews(limit int) ([]storage.FailedItem, error) {
	return nil, nil
}

func (f *FileCacheAdapter) DeleteFailedNews(id int) error {
	return nil
}

func (f *FileCacheAdapter) IncrementFailedAttempts(id int, errorMsg string) error {
	return nil
}

func (f *FileCacheAdapter) EnqueueDelayedPost(hash string, title, link, newsJSON string, delay time.Duration) error {
	return f.cache.EnqueueDelayedPost(hash, title, link, newsJSON, delay)
}

func (f *FileCacheAdapter) GetReadyDelayedPosts(ctx context.Context) ([]storage.DelayedPost, error) {
	return f.cache.GetReadyDelayedPosts()
}

func (f *FileCacheAdapter) MarkDelayedPostSent(id int) error {
	return f.cache.MarkDelayedPostSent(id)
}

func (f *FileCacheAdapter) MarkDelayedPostFailed(id int, errMsg string) error {
	return f.cache.MarkDelayedPostFailed(id, errMsg)
}

func (f *FileCacheAdapter) TryReservePublish(hash, title, url string) (bool, error) {
	return f.cache.TryReservePublish(hash, title, url)
}

func (f *FileCacheAdapter) CancelPublishReservation(hash string) error {
	return f.cache.CancelPublishReservation(hash)
}

func (f *FileCacheAdapter) GetState(key string) (string, error) {
	return f.cache.GetState(key)
}

func (f *FileCacheAdapter) SetState(key, value string) error {
	return f.cache.SetState(key, value)
}

func (f *FileCacheAdapter) GetCachedAITranslation(hash string) (string, error) {
	return f.cache.GetCachedAITranslation(hash)
}

func (f *FileCacheAdapter) SaveCachedAITranslation(hash, link, title, responseJSON string) error {
	return f.cache.SaveCachedAITranslation(hash, link, title, responseJSON)
}

// PostgresCacheAdapter —  партнер (Postgres).
// Он реально выполняет работу, вызывая методы из storage.
type PostgresCacheAdapter struct {
	cache *storage.PostgresCache
}

func (p *PostgresCacheAdapter) GenerateNewsHash(title, link string) string {
	return p.cache.GenerateNewsHash(title, link)
}

func (p *PostgresCacheAdapter) IsAlreadySent(hash string) bool {
	return p.cache.IsAlreadySent(hash)
}

func (p *PostgresCacheAdapter) IsSourceURLSent(sourceURL string) bool {
	return p.cache.IsSourceURLSent(sourceURL)
}

func (p *PostgresCacheAdapter) IsContentDuplicate(content string) (bool, string) {
	return p.cache.IsContentDuplicate(content)
}

func (p *PostgresCacheAdapter) IsTitleNearDuplicate(title string) (bool, string) {
	return p.cache.IsTitleNearDuplicate(title)
}

func (p *PostgresCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	return p.cache.MarkAsSent(hash, title, link, category, source)
}

func (p *PostgresCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	return p.cache.MarkAsSentWithContent(hash, title, link, content, category, source)
}

func (p *PostgresCacheAdapter) MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey string, emb []float32) error {
	return p.cache.MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey, emb)
}

func (p *PostgresCacheAdapter) CheckSemanticDuplicate(clusterKey string, candidateEmbedding []float32, titleUA string, lookback time.Duration, keyThreshold, cosineThreshold float64, shadowMode bool) (storage.SemanticCheckResult, error) {
	return p.cache.CheckSemanticDuplicate(clusterKey, candidateEmbedding, titleUA, lookback, keyThreshold, cosineThreshold, shadowMode)
}

func (p *PostgresCacheAdapter) GetRecentStories(lookback time.Duration) ([]storage.SentStoryRecord, error) {
	return p.cache.GetRecentStories(lookback)
}

func (p *PostgresCacheAdapter) SaveTelegramMessageID(hash string, msgID int) error {
	return p.cache.SaveTelegramMessageID(hash, msgID)
}

// --- РЕАЛИЗАЦИЯ DLQ ---
func (p *PostgresCacheAdapter) SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error {
	return p.cache.SaveFailedNews(title, link, imageURL, messageText, errorMsg)
}

func (p *PostgresCacheAdapter) GetFailedNews(limit int) ([]storage.FailedItem, error) {
	return p.cache.GetFailedNews(limit)
}

func (p *PostgresCacheAdapter) DeleteFailedNews(id int) error {
	return p.cache.DeleteFailedNews(id)
}

func (p *PostgresCacheAdapter) IncrementFailedAttempts(id int, errorMsg string) error {
	return p.cache.IncrementFailedAttempts(id, errorMsg)
}

// --- РЕАЛИЗАЦИЯ SUPABASE SYNC QUEUE ---

func (p *PostgresCacheAdapter) MarkSupabaseSynced(hash string) error {
	return p.cache.MarkSupabaseSynced(hash)
}

func (p *PostgresCacheAdapter) IsFunFactRecentlyUsed(funFact string) bool {
	return p.cache.IsFunFactRecentlyUsed(funFact)
}

func (p *PostgresCacheAdapter) MarkFunFactUsed(funFact string) error {
	return p.cache.MarkFunFactUsed(funFact)
}

func (p *PostgresCacheAdapter) EnqueueSupabaseSync(hash string, payload []byte) error {
	return p.cache.EnqueueSupabaseSync(hash, payload)
}

func (p *PostgresCacheAdapter) GetPendingSupabaseSync(limit int) ([]storage.SyncQueueItem, error) {
	return p.cache.GetPendingSupabaseSync(limit)
}

func (p *PostgresCacheAdapter) DeleteSyncQueueItem(id int) error {
	return p.cache.DeleteSyncQueueItem(id)
}

func (p *PostgresCacheAdapter) IncrementSyncQueueAttempts(id int, errMsg string) error {
	return p.cache.IncrementSyncQueueAttempts(id, errMsg)
}

// --- РЕАЛИЗАЦИЯ DELAYED POSTS QUEUE ---

func (p *PostgresCacheAdapter) EnqueueDelayedPost(hash string, title, link, newsJSON string, delay time.Duration) error {
	return p.cache.EnqueueDelayedPost(hash, title, link, newsJSON, delay)
}

func (p *PostgresCacheAdapter) GetReadyDelayedPosts(ctx context.Context) ([]storage.DelayedPost, error) {
	return p.cache.GetReadyDelayedPosts(ctx)
}

func (p *PostgresCacheAdapter) MarkDelayedPostSent(id int) error {
	return p.cache.MarkDelayedPostSent(id)
}

func (p *PostgresCacheAdapter) MarkDelayedPostFailed(id int, errMsg string) error {
	return p.cache.MarkDelayedPostFailed(id, errMsg)
}

func (p *PostgresCacheAdapter) TryReservePublish(hash, title, url string) (bool, error) {
	return p.cache.TryReservePublish(hash, title, url)
}

func (p *PostgresCacheAdapter) CancelPublishReservation(hash string) error {
	return p.cache.CancelPublishReservation(hash)
}

func (p *PostgresCacheAdapter) GetState(key string) (string, error) {
	return p.cache.GetState(key)
}

func (p *PostgresCacheAdapter) SetState(key, value string) error {
	return p.cache.SetState(key, value)
}

func (p *PostgresCacheAdapter) GetCachedAITranslation(hash string) (string, error) {
	return p.cache.GetCachedAITranslation(hash)
}

func (p *PostgresCacheAdapter) SaveCachedAITranslation(hash, link, title, responseJSON string) error {
	return p.cache.SaveCachedAITranslation(hash, link, title, responseJSON)
}
