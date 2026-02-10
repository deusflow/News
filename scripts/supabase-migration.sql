-- Migration script for Supabase news_archive table
-- Run this in Supabase SQL Editor before deploying the updated bot
--
-- This adds new columns required by the refactored bot:
-- - tldr: One-sentence summary
-- - fun_fact: Interesting fact about Denmark
-- - title_ukrainian: Ukrainian translation of title
-- - mood: News sentiment (positive, negative, neutral, shocking, urgent)
-- - tags: Array of Ukrainian tags

-- Add new columns if they don't exist
ALTER TABLE news_archive
ADD COLUMN IF NOT EXISTS tldr TEXT,
ADD COLUMN IF NOT EXISTS fun_fact TEXT,
ADD COLUMN IF NOT EXISTS title_ukrainian TEXT,
ADD COLUMN IF NOT EXISTS mood TEXT DEFAULT 'neutral',
ADD COLUMN IF NOT EXISTS tags TEXT[] DEFAULT '{}';

-- Add check constraint for mood values (optional but recommended)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'valid_mood'
    ) THEN
        ALTER TABLE news_archive
        ADD CONSTRAINT valid_mood
        CHECK (mood IN ('positive', 'negative', 'neutral', 'shocking', 'urgent') OR mood IS NULL);
    END IF;
END $$;

-- Create index for faster queries on mood and tags
CREATE INDEX IF NOT EXISTS idx_news_archive_mood ON news_archive(mood);
CREATE INDEX IF NOT EXISTS idx_news_archive_tags ON news_archive USING GIN(tags);

-- Verify columns were added
SELECT column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_name = 'news_archive'
AND column_name IN ('tldr', 'fun_fact', 'title_ukrainian', 'mood', 'tags')
ORDER BY ordinal_position;

-- Expected output:
-- | column_name      | data_type | is_nullable |
-- |------------------|-----------|-------------|
-- | tldr             | text      | YES         |
-- | fun_fact         | text      | YES         |
-- | title_ukrainian  | text      | YES         |
-- | mood             | text      | YES         |
-- | tags             | ARRAY     | YES         |

