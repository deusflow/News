package app

import (
	"time"

	"github.com/deusflow/News/internal/storage"
)

// CacheAdapter — это наш Контракт.
// Мы добавили сюда методы для DLQ (работы с ошибками), чтобы app.go мог их вызывать.
type CacheAdapter interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsLinkAlreadySent(link string) bool
	IsSourceURLSent(sourceURL string) bool // replaces Supabase REST duplicate check
	IsContentDuplicate(content string) (bool, string)
	IsTitleNearDuplicate(title string) (bool, string) // near-duplicate: same story from different source
	CheckSemanticDuplicate(clusterKey string, candidateEmbedding []float32, titleUA string, lookback time.Duration, keyThreshold, cosineThreshold float64, shadowMode bool) (storage.SemanticCheckResult, error)
	MarkAsSent(hash, title, link, category, source string) error
	MarkAsSentWithContent(hash, title, link, content, category, source string) error
	MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey string, emb []float32) error
	MarkSupabaseSynced(hash string) error
	IsFunFactRecentlyUsed(funFact string) bool
	MarkFunFactUsed(funFact string) error

	// Supabase sync queue — Neon is source of truth, Supabase is secondary
	EnqueueSupabaseSync(hash string, payload []byte) error
	GetPendingSupabaseSync(limit int) ([]storage.SyncQueueItem, error)
	DeleteSyncQueueItem(id int) error
	IncrementSyncQueueAttempts(id int, errMsg string) error

	// DLQ
	SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error
	GetFailedNews(limit int) ([]storage.FailedItem, error)
	DeleteFailedNews(id int) error
	IncrementFailedAttempts(id int, errorMsg string) error

	// Delayed Posts Queue (High-Impact second story)
	EnqueueDelayedPost(hash string, title, link, newsJSON string, delay time.Duration) error
	GetReadyDelayedPosts(ctx context.Context) ([]storage.DelayedPost, error)
	MarkDelayedPostSent(id int) error
	MarkDelayedPostFailed(id int, errMsg string) error
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

func (f *FileCacheAdapter) IsLinkAlreadySent(link string) bool {
	return false
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

func (p *PostgresCacheAdapter) IsLinkAlreadySent(link string) bool {
	return p.cache.IsLinkAlreadySent(link)
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
