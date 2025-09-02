#!/bin/bash
# Скрипт для быстрой настройки проекта

echo "🚀 Настройка Danish News Bot..."

# Проверяем, что Go установлен
if ! command -v go &> /dev/null; then
    echo "❌ Go не установлен. Установите Go 1.21+ и попробуйте снова."
    exit 1
fi

echo "✅ Go найден: $(go version)"

# Устанавливаем зависимости
echo "📦 Установка зависимостей..."
go mod download
go mod tidy

# Создаем .env файл если его нет
if [ ! -f .env ]; then
    echo "📝 Создание .env файла..."
    cp .env.example .env
    echo "⚠️  Отредактируйте .env файл, добавив ваши токены!"
fi

# Создаем папку для билдов
mkdir -p bin

echo "🎉 Настройка завершена!"
echo ""
echo "📋 Следующие шаги:"
echo "1. Отредактируйте .env файл (добавьте TELEGRAM_TOKEN и TELEGRAM_CHAT_ID)"
echo "2. Запустите: make run-local"
echo "3. Для GitHub Actions добавьте секреты в репозиторий"
echo ""
echo "🔗 Полезные команды:"
echo "  make run-local  - запуск с .env файлом"
echo "  make test      - тестовый прогон"
echo "  make build     - сборка исполняемого файла"
