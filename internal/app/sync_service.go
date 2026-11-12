package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/storage"
)

// saveToSupabase saves news to Supabase.
// On success → marks the row in Neon as supabase_synced=TRUE.
// On failure → enqueues payload in supabase_sync_queue for retry next run.
// The duplicate check against Supabase has been removed: Neon IsSourceURLSent is called
// before Telegram send, so by the time we reach here the news is guaranteed unique.
func saveToSupabase(ctx context.Context, queue SyncQueueStore, client *storage.SupabaseClient, hash string, n news.News) {
	archive := storage.NewsArchive{
		Slug:             storage.GenerateSlugWithDate(n.Title, n.Published),
		Title:            n.Title,
		TitleUkrainian:   n.TitleUkrainian,
		SummaryUkrainian: n.SummaryUkrainian,
		SummaryDanish:    n.SummaryDanish,
		TLDR:             n.TLDR,
		FunFact:          n.FunFact,
		WhyItMatters:     n.WhyItMatters,
		ImageURL:         n.ImageURL,
		SourceURL:        n.Link,
		SourceName:       n.SourceName,
		Category:         n.Category,
		Tags:             n.Tags,
		Mood:             n.Mood,
		PublishedAt:      n.Published,
	}

	// Detach from parent cancellation so post-publication DB/Supabase sync is non-cancelable.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	if err := client.SaveNews(saveCtx, archive); err != nil {
		logger.Warn("Failed to save to Supabase, enqueuing for retry",
			"title", n.Title, "error", err)
		// Serialise payload so we can retry without AI/scraping again.
		if payload, jsonErr := marshalSyncPayload(archive); jsonErr == nil {
			if qErr := queue.EnqueueSupabaseSync(hash, payload); qErr != nil {
				logger.Error("Failed to enqueue Supabase sync", "hash", hash, "error", qErr)
			}
		}
	} else {
		logger.Info("Saved to Supabase archive", "title", n.Title)
		if sErr := queue.MarkSupabaseSynced(hash); sErr != nil {
			logger.Warn("Failed to mark supabase_synced in Neon", "hash", hash, "error", sErr)
		}
	}
}

// marshalSyncPayload serialises a NewsArchive to JSON for the sync queue.
func marshalSyncPayload(archive storage.NewsArchive) ([]byte, error) {
	return json.Marshal(archive)
}

// unmarshalSyncPayload deserialises a NewsArchive from the sync queue payload.
func unmarshalSyncPayload(data []byte, out *storage.NewsArchive) error {
	return json.Unmarshal(data, out)
}

// syncPendingToSupabase flushes supabase_sync_queue entries from previous failed runs.
// Runs once per bot invocation, before the main fetch cycle.
func syncPendingToSupabase(ctx context.Context, queue SyncQueueStore, client *storage.SupabaseClient) {
	items, err := queue.GetPendingSupabaseSync(10)
	if err != nil {
		logger.Error("Failed to get pending Supabase sync items", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	logger.Info("Syncing pending items to Supabase", "count", len(items))

	for _, item := range items {
		var archive storage.NewsArchive
		if err := unmarshalSyncPayload(item.Payload, &archive); err != nil {
			logger.Error("Failed to unmarshal sync payload", "id", item.ID, "error", err)
			_ = queue.IncrementSyncQueueAttempts(item.ID, err.Error())
			continue
		}

		if err := client.SaveNews(ctx, archive); err != nil {
			logger.Warn("Supabase sync retry failed", "id", item.ID, "error", err)
			_ = queue.IncrementSyncQueueAttempts(item.ID, err.Error())
		} else {
			logger.Info("Supabase sync retry succeeded", "id", item.ID, "slug", archive.Slug)
			_ = queue.DeleteSyncQueueItem(item.ID)
			_ = queue.MarkSupabaseSynced(item.SentNewsHash)
		}
	}
}
