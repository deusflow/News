package app

import (
	"github.com/deusflow/News/internal/storage"
)

// CacheAdapter — это наш "Контракт".
// Любая база данных, которая хочет с нами работать, ОБЯЗАНА уметь делать всё, что тут написано.
type CacheAdapter interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsLinkAlreadySent(link string) bool
	IsContentDuplicate(content string) (bool, string)
	MarkAsSent(hash, title, link, category, source string) error
	MarkAsSentWithContent(hash, title, link, content, category, source string) error

	// --- НОВЫЕ ТРЕБОВАНИЯ (DLQ) ---
	// Теперь мы требуем уметь работать с ошибками (DLQ)
	SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error
	GetFailedNews(limit int) ([]storage.FailedItem, error)
	DeleteFailedNews(id int) error
	IncrementFailedAttempts(id int, errorMsg string) error
}

// FileCacheAdapter Он не умеет работать с ошибками, но чтобы контракт был подписан,
// он должен хотя бы делать вид (возвращать пустые значения).
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

func (f *FileCacheAdapter) IsContentDuplicate(content string) (bool, string) {
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

// --- ЗАГЛУШКИ ДЛЯ DLQ (FileCache просто говорит "Я ничего не сделал", но не ломается) ---

func (f *FileCacheAdapter) SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error {
	return nil // "Ок, я типа сохранил (на самом деле нет)"
}

func (f *FileCacheAdapter) GetFailedNews(limit int) ([]storage.FailedItem, error) {
	return nil, nil // "У меня нет ошибок"
}

func (f *FileCacheAdapter) DeleteFailedNews(id int) error {
	return nil
}

func (f *FileCacheAdapter) IncrementFailedAttempts(id int, errorMsg string) error {
	return nil
}

// PostgresCacheAdapter — это "Надежный партнер".
// Он реально умеет всё это делать, мы просто соединяем провода.
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

func (p *PostgresCacheAdapter) IsContentDuplicate(content string) (bool, string) {
	return p.cache.IsContentDuplicate(content)
}

func (p *PostgresCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	return p.cache.MarkAsSent(hash, title, link, category, source)
}

func (p *PostgresCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	return p.cache.MarkAsSentWithContent(hash, title, link, content, category, source)
}

// --- РЕАЛИЗАЦИЯ DLQ ДЛЯ POSTGRES ---
// Тут мы просто вызываем реальные методы из postgres.go

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
