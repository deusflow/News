#!/bin/bash
# generate-pages.sh
# Fetches news from Supabase and generates Hugo markdown files

set -e

SUPABASE_URL="${SUPABASE_URL}"
SUPABASE_KEY="${SUPABASE_SERVICE_KEY}"
CONTENT_DIR="website/content/posts"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}🚀 Starting page generation from Supabase...${NC}"

# Check required env vars
if [ -z "$SUPABASE_URL" ] || [ -z "$SUPABASE_KEY" ]; then
    echo -e "${RED}❌ SUPABASE_URL and SUPABASE_SERVICE_KEY are required${NC}"
    exit 1
fi

# Create content directory
mkdir -p "$CONTENT_DIR"

# Fetch active news (last 10 days, not archived)
echo -e "${YELLOW}📥 Fetching active news from Supabase...${NC}"

RESPONSE=$(curl -s -X GET \
    "${SUPABASE_URL}/rest/v1/news_archive?is_archived=eq.false&order=published_at.desc" \
    -H "apikey: ${SUPABASE_KEY}" \
    -H "Authorization: Bearer ${SUPABASE_KEY}")

# Check if response is valid JSON array
if ! echo "$RESPONSE" | jq -e 'type == "array"' > /dev/null 2>&1; then
    echo -e "${RED}❌ Invalid response from Supabase${NC}"
    echo "$RESPONSE"
    exit 1
fi

# Count items
COUNT=$(echo "$RESPONSE" | jq 'length')
echo -e "${GREEN}✅ Found ${COUNT} active news items${NC}"

if [ "$COUNT" -eq 0 ]; then
    echo -e "${YELLOW}ℹ️ No news to generate. Creating placeholder...${NC}"

    # Create a placeholder post
    cat > "$CONTENT_DIR/_index.md" << 'EOF'
---
title: "Новини"
---
EOF
    exit 0
fi

# Generate markdown files for each news item
echo "$RESPONSE" | jq -c '.[]' | while read -r item; do
    SLUG=$(echo "$item" | jq -r '.slug')
    TITLE=$(echo "$item" | jq -r '.title')
    TITLE_UK=$(echo "$item" | jq -r '.title_ukrainian // empty')
    SUMMARY_UK=$(echo "$item" | jq -r '.summary_ukrainian // empty')
    SUMMARY_DA=$(echo "$item" | jq -r '.summary_danish // empty')
    TLDR=$(echo "$item" | jq -r '.tldr // empty')
    FUN_FACT=$(echo "$item" | jq -r '.fun_fact // empty')
    IMAGE_URL=$(echo "$item" | jq -r '.image_url // empty')
    SOURCE_URL=$(echo "$item" | jq -r '.source_url // empty')
    SOURCE_NAME=$(echo "$item" | jq -r '.source_name // empty')
    CATEGORY=$(echo "$item" | jq -r '.category // "News"')
    MOOD=$(echo "$item" | jq -r '.mood // "neutral"')
    PUBLISHED_AT=$(echo "$item" | jq -r '.published_at')

    # Use placeholder if no image
    if [ -z "$IMAGE_URL" ] || [ "$IMAGE_URL" = "null" ]; then
        # Generate category-based placeholder
        case "$CATEGORY" in
            "politics"|"Politics")
                IMAGE_URL="https://placehold.co/800x600/1e3a5f/ffffff?text=🏛️+Politik"
                ;;
            "economy"|"Economy")
                IMAGE_URL="https://placehold.co/800x600/2d5016/ffffff?text=💰+Økonomi"
                ;;
            "culture"|"Culture")
                IMAGE_URL="https://placehold.co/800x600/5c1f5c/ffffff?text=🎭+Kultur"
                ;;
            "ukraine"|"Ukraine")
                IMAGE_URL="https://placehold.co/800x600/005bbb/ffd500?text=🇺🇦+Ukraine"
                ;;
            *)
                IMAGE_URL="https://placehold.co/800x600/23252b/FF0055?text=🇩🇰+DK+News"
                ;;
        esac
    fi

    # Generate filename
    DATE_PREFIX=$(echo "$PUBLISHED_AT" | cut -d'T' -f1)
    FILENAME="${DATE_PREFIX}-${SLUG}.md"
    FILEPATH="${CONTENT_DIR}/${FILENAME}"

    # Skip if file already exists
    if [ -f "$FILEPATH" ]; then
        continue
    fi

    echo -e "  📄 Generating: ${FILENAME}"

    # Create markdown file
    cat > "$FILEPATH" << EOF
---
title: "${TITLE//\"/\\\"}"
date: ${PUBLISHED_AT}
draft: false
categories: ["${CATEGORY}"]
image: "${IMAGE_URL}"
source_url: "${SOURCE_URL}"
source_name: "${SOURCE_NAME}"
tldr: "${TLDR//\"/\\\"}"
mood: "${MOOD}"
slug: "${SLUG}"
---

## 🇺🇦 Українською

${SUMMARY_UK:-"*Переклад очікується*"}

## 🇩🇰 På dansk

${SUMMARY_DA:-"*Oversættelse kommer snart*"}

EOF

    # Add fun fact if exists
    if [ -n "$FUN_FACT" ] && [ "$FUN_FACT" != "null" ]; then
        echo "---" >> "$FILEPATH"
        echo "" >> "$FILEPATH"
        echo "💡 **Цікавий факт:** ${FUN_FACT}" >> "$FILEPATH"
    fi

done

# Generate _index.md for posts section
cat > "$CONTENT_DIR/_index.md" << 'EOF'
---
title: "Архів новин"
---
EOF

echo -e "${GREEN}✅ Page generation completed!${NC}"
echo -e "${GREEN}📁 Generated files in ${CONTENT_DIR}${NC}"
ls -la "$CONTENT_DIR"
