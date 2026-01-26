#!/bin/bash
# test-supabase.sh - Перевірка новин в Supabase
#
# Використання:
#   SUPABASE_URL="https://xxx.supabase.co" SUPABASE_SERVICE_KEY="eyJ..." ./scripts/test-supabase.sh

if [ -z "$SUPABASE_URL" ] || [ -z "$SUPABASE_SERVICE_KEY" ]; then
    echo "❌ Потрібно встановити SUPABASE_URL та SUPABASE_SERVICE_KEY"
    echo ""
    echo "Приклад:"
    echo '  SUPABASE_URL="https://xxx.supabase.co" SUPABASE_SERVICE_KEY="eyJ..." ./scripts/test-supabase.sh'
    exit 1
fi

echo "🔍 Перевіряю новини в Supabase..."
echo "URL: $SUPABASE_URL"
echo ""

RESPONSE=$(curl -s -X GET \
    "${SUPABASE_URL}/rest/v1/news_archive?order=created_at.desc&limit=5" \
    -H "apikey: ${SUPABASE_SERVICE_KEY}" \
    -H "Authorization: Bearer ${SUPABASE_SERVICE_KEY}")

# Перевірка чи є відповідь
if [ -z "$RESPONSE" ]; then
    echo "❌ Немає відповіді від Supabase"
    exit 1
fi

# Перевірка на помилку
if echo "$RESPONSE" | grep -q "error"; then
    echo "❌ Помилка Supabase:"
    echo "$RESPONSE" | jq .
    exit 1
fi

# Підрахунок новин
COUNT=$(echo "$RESPONSE" | jq 'length')

if [ "$COUNT" -eq 0 ]; then
    echo "📭 Таблиця news_archive порожня"
    echo "   Новини ще не були збережені ботом"
else
    echo "✅ Знайдено $COUNT новин в базі:"
    echo ""
    echo "$RESPONSE" | jq -r '.[] | "📰 \(.title)\n   📅 \(.created_at)\n   🔗 \(.source_url)\n"'
fi
