#!/bin/bash
# Скрипт запуска с автоматической загрузкой .env

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

echo "✅ Переменные окружения загружены"
echo "🎯 Канал: $TELEGRAM_CHAT_ID"

# Запускаем приложение
echo "▶️  Запуск приложения..."
go run cmd/dknews/main.go
