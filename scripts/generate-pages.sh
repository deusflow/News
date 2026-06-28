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

    # Parse tags array into Hugo-compatible format: ["tag1", "tag2"]
    # If tags is null or empty, use empty array
    TAGS_JSON=$(echo "$item" | jq -c '.tags // []')
    if [ "$TAGS_JSON" = "null" ] || [ "$TAGS_JSON" = "[]" ]; then
        TAGS_LINE=""
    else
        TAGS_LINE="tags: ${TAGS_JSON}"
    fi

    # Use placeholder if no image
    if [ -z "$IMAGE_URL" ] || [ "$IMAGE_URL" = "null" ]; then
        # Generate category-based placeholder
        case "${CATEGORY,,}" in
            "politics")
                IMAGE_URL="https://images.unsplash.com/photo-1529107386315-e1a2ed48a620?w=1600&h=900&fit=crop"
                ;;
            "economy")
                IMAGE_URL="https://images.unsplash.com/photo-1611974789855-9c2a0a7236a3?w=1600&h=900&fit=crop"
                ;;
            "visas")
                IMAGE_URL="https://images.unsplash.com/photo-1555848962-6e79363ec58f?w=1600&h=900&fit=crop"
                ;;
            "work")
                IMAGE_URL="https://images.unsplash.com/photo-1521737604893-d14cc237f11d?w=1600&h=900&fit=crop"
                ;;
            "money")
                IMAGE_URL="https://images.unsplash.com/photo-1580519542036-ed47f3e42214?w=1600&h=900&fit=crop"
                ;;
            "society"|"culture")
                IMAGE_URL="https://images.unsplash.com/photo-1517486808906-6ca8b3f04846?w=1600&h=900&fit=crop"
                ;;
            "war")
                IMAGE_URL="https://images.unsplash.com/photo-1503460867056-11f421f57ec0?w=1600&h=900&fit=crop"
                ;;
            "local")
                IMAGE_URL="https://images.unsplash.com/photo-1513622470522-26c308a371e5?w=1600&h=900&fit=crop"
                ;;
            "education")
                IMAGE_URL="https://images.unsplash.com/photo-1523050854058-8df90110c9f1?w=1600&h=900&fit=crop"
                ;;
            "crime")
                IMAGE_URL="https://images.unsplash.com/photo-1589829085413-56de8ae18c73?w=1600&h=900&fit=crop"
                ;;
            "tech")
                IMAGE_URL="https://images.unsplash.com/photo-1518770660439-4636190af475?w=1600&h=900&fit=crop"
                ;;
            "family")
                IMAGE_URL="https://images.unsplash.com/photo-1511895426328-dc8714191300?w=1600&h=900&fit=crop"
                ;;
            "lifestyle")
                IMAGE_URL="https://images.unsplash.com/photo-1514933651103-005eec06c04b?w=1600&h=900&fit=crop"
                ;;
            "sport")
                IMAGE_URL="https://images.unsplash.com/photo-1461896836934-ffe607ba8211?w=1600&h=900&fit=crop"
                ;;
            "eu")
                IMAGE_URL="https://images.unsplash.com/photo-1555909241-1555776d6537?w=1600&h=900&fit=crop"
                ;;
            "ukraine")
                IMAGE_URL="https://images.unsplash.com/photo-1513622470522-26c308a371e5?w=1600&h=900&fit=crop"
                ;;
            *)
                IMAGE_URL="https://images.unsplash.com/photo-1513622470522-26c308a371e5?w=1600&h=900&fit=crop"
                ;;
        esac
    fi

    # Generate filename
    DATE_PREFIX=$(echo "$PUBLISHED_AT" | cut -d'T' -f1)
    FILENAME="${DATE_PREFIX}-${SLUG}.md"
    FILEPATH="${CONTENT_DIR}/${FILENAME}"

    # Extract base slug (without date suffix) for duplicate check
    # Example: "putins-spinmaskine-falsk-mordhistorie-om-trump-og-groenland-20260128"
    # We want to check if ANY file with similar slug exists
    BASE_SLUG=$(echo "$SLUG" | sed 's/-[0-9]\{8\}$//')  # Remove trailing -YYYYMMDD

    # Check for duplicates: any file containing this base slug
    EXISTING_FILES=$(find "$CONTENT_DIR" -name "*${BASE_SLUG}*.md" 2>/dev/null | head -1)

    if [ -n "$EXISTING_FILES" ]; then
        echo -e "  ⏭️ Skipping (similar file exists): ${BASE_SLUG}"
        continue
    fi

    # Also check exact filename (original check)
    if [ -f "$FILEPATH" ]; then
        continue
    fi

    echo -e "  📄 Generating: ${FILENAME}"

    # Create markdown file with front matter
    # Build front matter dynamically to handle optional tags
    {
        echo "---"
        echo "title: \"${TITLE//\"/\\\"}\""
        echo "date: ${PUBLISHED_AT}"
        echo "draft: false"
        echo "categories: [\"${CATEGORY}\"]"
        # Add tags only if they exist
        if [ -n "$TAGS_LINE" ]; then
            echo "$TAGS_LINE"
        fi
        echo "image: \"${IMAGE_URL}\""
        echo "source_url: \"${SOURCE_URL}\""
        echo "source_name: \"${SOURCE_NAME}\""
        echo "tldr: \"${TLDR//\"/\\\"}\""
        echo "mood: \"${MOOD}\""
        echo "slug: \"${SLUG}\""
        echo "---"
        echo ""
        echo "## 🇺🇦 Українською"
        echo ""
        echo "${SUMMARY_UK:-"*Переклад очікується*"}"
        echo ""
        echo "## 🇩🇰 På dansk"
        echo ""
        echo "${SUMMARY_DA:-"*Oversættelse kommer snart*"}"
        echo ""
    } > "$FILEPATH"

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
