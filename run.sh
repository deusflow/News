#!/bin/bash
# =============================================================================
# run.sh — локальный запуск Danish News Bot
# =============================================================================
# Использование: ./run.sh
#
# Что делает:
#   1. Проверяет наличие .env файла (создайте из .env.example)
#   2. Экспортирует переменные окружения из .env
#   3. Запускает бот через `go run cmd/dknews/main.go`
#
# Обязательные переменные в .env:
#   TELEGRAM_TOKEN     — токен Telegram бота (от @BotFather)
#   TELEGRAM_CHAT_ID   — ID канала (например @my_channel или -1001234567890)
#   GEMINI_API_KEY     — ключ Gemini API (console.cloud.google.com)
#   DATABASE_URL       — строка подключения к Neon PostgreSQL
#
# Для production используйте GitHub Actions (см. .github/workflows/)
# =============================================================================
set -e

echo "🚀 Запуск Danish News Bot..."

# Проверяем наличие .env файла
if [ ! -f .env ]; then
    echo "❌ Файл .env не найден!"
    echo "📝 Создайте .env файл из .env.example"
    exit 1
fi

# Загружаем переменные окружения из .env
echo "📋 Загружаем переменные окружения..."
export $(cat .env | grep -v '^#' | xargs)

# Проверяем обязательные переменные
if [ -z "$TELEGRAM_TOKEN" ]; then
    echo "❌ TELEGRAM_TOKEN не установлен в .env файле"
    exit 1
fi

if [ -z "$TELEGRAM_CHAT_ID" ]; then
    echo "❌ TELEGRAM_CHAT_ID не установлен в .env файле"
    exit 1
fi

if [ -z "$GEMINI_MODEL" ]; then
    export GEMINI_MODEL="gemini-3.5-flash"
    echo "ℹ️ GEMINI_MODEL не задан, используем $GEMINI_MODEL"
fi

echo "✅ Переменные окружения загружены"
echo "🎯 Канал: $TELEGRAM_CHAT_ID"

# Запускаем приложение
echo "▶️  Запуск приложения..."
go run cmd/dknews/main.go
