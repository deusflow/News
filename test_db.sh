#!/bin/bash
# =============================================================================
# test_db.sh — проверка подключения к PostgreSQL (Neon)
# =============================================================================
# Использование: ./test_db.sh
#
# Что делает:
#   1. Загружает DATABASE_URL из .env
#   2. Подключается к Neon PostgreSQL
#   3. Показывает статистику БД и последние 5 отправленных новостей
#   4. Тестирует генерацию hash и проверку дубликатов
#
# Требует: DATABASE_URL в .env или в окружении
# =============================================================================

# Load environment variables
if [ -f .env ]; then
    export $(cat .env | grep -v '^#' | xargs)
fi

echo "🧪 Testing PostgreSQL Database Connection..."
echo ""

# Run the test (moved from root to cmd/testdb/ to avoid package main conflict)
go run cmd/testdb/main.go
