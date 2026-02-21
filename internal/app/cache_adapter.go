package app

import (
	"github.com/deusflow/News/internal/storage"
)

// CacheAdapter — это наш Контракт.
// Мы добавили сюда методы для DLQ (работы с ошибками), чтобы app.go мог их вызывать.
type CacheAdapter interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsLinkAlreadySent(link string) bool
	IsContentDuplicate(content string) (bool, string)
	MarkAsSent(hash, title, link, category, source string) error
	MarkAsSentWithContent(hash, title, link, content, category, source string) error

	// --- ВОТ ЭТИ МЕТОДЫ ТЫ ЗАБЫЛ ДОБАВИТЬ В ПРОШЛЫЙ РАЗ ---
	SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error
	GetFailedNews(limit int) ([]storage.FailedItem, error)
	DeleteFailedNews(id int) error
	IncrementFailedAttempts(id int, errorMsg string) error
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

func (p *PostgresCacheAdapter) IsContentDuplicate(content string) (bool, string) {
	return p.cache.IsContentDuplicate(content)
}

func (p *PostgresCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	return p.cache.MarkAsSent(hash, title, link, category, source)
}

func (p *PostgresCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	return p.cache.MarkAsSentWithContent(hash, title, link, content, category, source)
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
