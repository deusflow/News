package app

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai/embedding"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/publisher"
	"github.com/deusflow/News/internal/storage"
)

// getPublishLimit returns the maximum number of news items to publish per run.
// Defaults to 1 (single post per run). Can be overridden via PUBLISH_LIMIT or MAX_NEWS_LIMIT env vars.
func getPublishLimit() int {
	if val := os.Getenv("PUBLISH_LIMIT"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			return limit
		}
	}
	if val := os.Getenv("MAX_NEWS_LIMIT"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			return limit
		}
	}
	return 1
}

// validCandidate represents a candidate vetted for publication
type validCandidate struct {
	news news.News
	hash string
	rank int // 1-based original rank in candidates list
}

// sendBestNews publishes up to publishPerRunLimit news items — the highest-scored non-duplicates.
// newsList MUST already be sorted by score descending (done by FilterAndTranslateWithOptions).
func sendBestNews(ctx context.Context, newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, supabase *storage.SupabaseClient, embedder embedding.Embedder) {
	candidates := newsList
	if cfg.Feature.EnablePublicImpactGate {
		impactCandidates := make([]news.News, 0, len(newsList))
		for _, n := range newsList {
			if news.PassesAudienceRelevanceGate(n) {
				impactCandidates = append(impactCandidates, n)
			}
		}
		if len(impactCandidates) > 0 {
			logger.Info("Public impact gate enabled: using only relevance-checked candidates",
				"before", len(newsList),
				"after", len(impactCandidates))
			candidates = impactCandidates
		} else {
			logger.Info("Public impact gate enabled: NO candidates passed the audience relevance gate, returning to avoid publishing noise",
				"candidates", len(newsList))
			return
		}
	}

	publishLimit := getPublishLimit()

	// In-memory session deduplication guard for this run
	sessionHashes := make(map[string]bool)
	sessionUrls := make(map[string]bool)

	// Embedding budget: per-run cap on Gemini embedding calls (sentinel -1 = unlimited)
	embeddingBudget := -1
	if cfg.SemanticDedup.MaxEmbeddingRequests > 0 {
		embeddingBudget = cfg.SemanticDedup.MaxEmbeddingRequests
	}

	var valid []validCandidate

	recentStories, err := cacheAdapter.GetRecentStories(24 * time.Hour)
	if err != nil {
		logger.Warn("Failed to fetch recent stories for fatigue and developing checks", "error", err)
	}

	// 1. Category Anti-Fatigue check:
	// If the last published post was politics, war, or crime, penalize candidates in that same category
	// by -15 points so the channel naturally rotates topics (family, schools, society, work, lifestyle).
	var lastCategory news.Category
	if len(recentStories) > 0 {
		lastCategory = news.ValidateCategory(recentStories[0].Category)
		logger.Info("Last published story detected",
			"title", recentStories[0].Title,
			"category", lastCategory,
			"sent_at", recentStories[0].SentAt)
	}

	for i, n := range candidates {
		if len(valid) > publishLimit {
			break // We have enough: publishLimit to send + 1 for diff log
		}

		// Apply category anti-fatigue penalty if repeating politics/war/crime
		if ApplyCategoryAntiFatigue(&n, lastCategory) {
			logger.Info("Applied category anti-fatigue penalty (-15)",
				"candidate_title", n.Title,
				"category", n.Category,
				"adjusted_score", n.Score)
		}

		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if sessionHashes[hash] || (n.Link != "" && sessionUrls[n.Link]) {
			logger.Info("⏭️ Skipping session in-memory duplicate", "title", n.Title, "link", n.Link)
			continue
		}

		// In-session cluster deduplication guard: never select two articles of the same story cluster in the same run
		isSameSessionCluster := false
		for _, v := range valid {
			if n.StoryClusterKey != "" && v.news.StoryClusterKey != "" {
				sim := embedding.ClusterKeySimilarity(n.StoryClusterKey, v.news.StoryClusterKey)
				if sim >= cfg.SemanticDedup.ClusterKeyThreshold {
					isSameSessionCluster = true
					break
				}
			}
		}
		if isSameSessionCluster {
			logger.Info("⏭️ Skipping candidate with same story cluster as already selected candidate in this run",
				"title", n.Title, "cluster_key", n.StoryClusterKey)
			continue
		}
		logger.Info("evaluating publish candidate",
			"rank", i+1,
			"title", n.Title,
			"score", n.Score,
			"impact_score", n.ImpactScore,
			"keyword_score", n.KeywordScore,
			"category", n.Category,
			"source", n.SourceName,
			"hash", hash,
			"link", n.Link,
			"content_len", len([]rune(strings.TrimSpace(n.Content))),
			"core_impact_score", n.CoreImpactScore,
			"soft_score", n.SoftScore,
			"editorial_adjustment", n.EditorialAdjustment,
			"passes_public_impact_gate", news.PassesPublicImpactGate(n),
			"passes_audience_gate", news.PassesAudienceRelevanceGate(n))

		// ═══ Duplicate-policy gates ═══
		// Policy: fail-CLOSED for cheap exact identifiers (hash, source_url) — on DB
		// error these gates assume "already sent" to prevent duplicate spam in Telegram.
		// fail-OPEN for fuzzy heuristics (title/content/semantic) — on DB error they
		// assume "not duplicate" so one DB glitch cannot silence the whole feed.
		if cacheAdapter.IsAlreadySent(hash) {
			logger.Info("⏭️ Skipping already-sent hash", "title", n.Title, "hash", hash)
			continue
		}
		if cacheAdapter.IsSourceURLSent(n.Link) {
			logger.Info("⏭️ Skipping already-sent source_url (Neon dedup)", "title", n.Title, "source_url", n.Link)
			m.IncrementDuplicatesFiltered()
			continue
		}
		if isDuplicate, existingTitle := cacheAdapter.IsTitleNearDuplicate(n.Title); isDuplicate {
			logger.Info("⏭️ Skipping near-duplicate title (same story, different source)",
				"new_title", n.Title,
				"existing_title", existingTitle)
			m.IncrementDuplicatesFiltered()
			continue
		}
		if isDuplicate, existingTitle := cacheAdapter.IsContentDuplicate(n.Content); isDuplicate {
			logger.Info("⏭️ Skipping duplicate news (same content found)",
				"new_title", n.Title,
				"existing_title", existingTitle,
				"content_len", len([]rune(strings.TrimSpace(n.Content))))
			m.IncrementDuplicatesFiltered()
			continue
		}

		// Two-Tier Semantic Deduplication (Tier 1: story_cluster_key, Tier 2: text-embedding-004)
		if cfg.SemanticDedup.Enable {
			lookback := time.Duration(cfg.SemanticDedup.LookbackDays) * 24 * time.Hour
			if lookback <= 0 {
				lookback = 7 * 24 * time.Hour
			}

			// Generate embedding for candidate if embedder is available, budget allows, and vector not yet computed
			if embedder != nil && len(n.Embedding) == 0 && embeddingBudget != 0 {
				textToEmbed := n.TitleUkrainian
				if n.TLDR != "" {
					textToEmbed += ". " + n.TLDR
				}
				emb, err := embedder.Embed(ctx, textToEmbed)
				if err != nil {
					logger.Warn("Failed to generate embedding for candidate (falling back to Tier 1 cluster key)",
						"title", n.TitleUkrainian,
						"error", err)
				} else {
					n.Embedding = emb
					if embeddingBudget > 0 {
						embeddingBudget--
						if embeddingBudget == 0 {
							logger.Warn("Embedding request budget exhausted for this run, further dedup falls back to Tier 1 cluster key",
								"budget", cfg.SemanticDedup.MaxEmbeddingRequests)
						}
					}
				}
			} else if embedder != nil && len(n.Embedding) == 0 {
				logger.Info("Embedding budget exhausted, skipping embedding (Tier 1 cluster key only)",
					"title", n.TitleUkrainian)
			}

			semRes, err := cacheAdapter.CheckSemanticDuplicate(
				n.StoryClusterKey,
				n.Embedding,
				n.TitleUkrainian,
				lookback,
				cfg.SemanticDedup.ClusterKeyThreshold,
				cfg.SemanticDedup.Threshold,
				cfg.SemanticDedup.ShadowMode,
			)
			if err == nil {
				if semRes.ShadowMode && semRes.WouldReject && !semRes.IsDuplicate {
					logger.Info("🔍 [SHADOW DEDUP] High semantic similarity detected (would reject if shadow mode was false)",
						"new_title", n.TitleUkrainian,
						"cluster_key", n.StoryClusterKey,
						"matched_title", semRes.MatchedTitle,
						"cosine_similarity", semRes.CosineSimilarity,
						"cluster_similarity", semRes.ClusterSimilarity,
						"trigger", semRes.Trigger,
						"threshold", cfg.SemanticDedup.Threshold)
				}

				if semRes.IsDuplicate {
					approved, replyToID, dropReason := EvaluateDevelopingStory(n, recentStories, cfg.SemanticDedup.ClusterKeyThreshold, cfg.SemanticDedup.Threshold, cfg.SemanticDedup.ShadowMode)
					if approved {
						n.ReplyToMessageID = replyToID
						logger.Info("🔄 Developing story update approved (+concrete decision)",
							"title", n.TitleUkrainian,
							"cluster_key", n.StoryClusterKey,
							"reply_to_msg_id", n.ReplyToMessageID,
							"anchor", n.ConcreteAnchor)
						// Proceed to add as valid candidate!
					} else {
						logger.Info("⏭️ Skipping story",
							"reason", dropReason,
							"new_title", n.TitleUkrainian,
							"cluster_key", n.StoryClusterKey,
							"matched_title", semRes.MatchedTitle,
							"trigger", semRes.Trigger,
							"cluster_similarity", semRes.ClusterSimilarity,
							"cosine_similarity", semRes.CosineSimilarity)
						m.IncrementDuplicatesFiltered()
						continue
					}
				}
			}
		}

		sessionHashes[hash] = true
		if n.Link != "" {
			sessionUrls[n.Link] = true
		}
		valid = append(valid, validCandidate{news: n, hash: hash, rank: i + 1})
	}

	if len(valid) == 0 {
		logger.Info("No publishable news found in this run")
		return
	}

	// Re-sort valid candidates by score after anti-fatigue adjustments
	sort.SliceStable(valid, func(i, j int) bool {
		return valid[i].news.Score > valid[j].news.Score
	})

	// Night quiet hours guard: skip publication to public Telegram channel between 22:30 and 07:30 Copenhagen time.
	if IsCopenhagenQuietHours(time.Now()) {
		logger.Info("🌙 Copenhagen quiet hours (22:30 - 07:30) active: skipping publication to avoid disturbing subscribers at night")
		return
	}

	// Safety: if only one low-score candidate, skip to avoid noise
	if len(valid) == 1 && valid[0].news.Score < 70 {
		logger.Info("Only one low-score candidate, skipping run to avoid publishing noise",
			"score", valid[0].news.Score, "title", valid[0].news.Title)
		return
	}

	// Determine how many to actually publish
	toPublish := min(publishLimit, len(valid))

	// Log winner vs runner-up diff
	diffLog := []interface{}{
		"winner_title", valid[0].news.Title,
		"winner_score", valid[0].news.Score,
		"winner_category", valid[0].news.Category,
		"winner_kw_score", valid[0].news.KeywordScore,
		"winner_original_rank", valid[0].rank,
		"total_to_publish", toPublish,
	}
	nextIdx := toPublish // first item NOT being published
	if nextIdx < len(valid) {
		diffLog = append(diffLog,
			"next_title", valid[nextIdx].news.Title,
			"next_score", valid[nextIdx].news.Score,
			"next_category", valid[nextIdx].news.Category,
			"score_diff", valid[0].news.Score-valid[nextIdx].news.Score)
	} else {
		diffLog = append(diffLog, "next_title", "none")
	}
	logger.Info("Winner reason & evaluation result", diffLog...)

	// Initialize publisher
	tgPublisher := publisher.NewTelegramPublisher(cfg, cacheAdapter, m)

	// Publish each item
	for idx := 0; idx < toPublish; idx++ {
		vc := valid[idx]

		if vc.rank > 1 && idx == 0 {
			logger.Warn("Quality drop detected: first publication went to rank > 1 due to dedup of top candidates",
				"published_rank", vc.rank,
				"skipped_count", vc.rank-1,
				"published_title", vc.news.Title)
		}

		logger.Info("Publishing news",
			"publish_index", idx+1,
			"of", toPublish,
			"title", vc.news.Title,
			"score", vc.news.Score,
			"category", vc.news.Category,
			"rank", vc.rank)

		_, success := tgPublisher.Publish(ctx, vc.news, vc.hash)
		if success {
			if supabase != nil {
				saveToSupabase(ctx, cacheAdapter, supabase, vc.hash, vc.news)
			}
		}

		// Brief pause between sends to avoid Telegram flood limits
		if idx < toPublish-1 {
			time.Sleep(5 * time.Second)
		}
	}

	// Adaptive High-Impact Queue: If there's a 2nd candidate with ImpactScore >= 85
	// that wasn't published in this run, enqueue it to delayed_posts with a 30m delay.
	if toPublish == 1 && len(valid) >= 2 {
		secondCandidate := valid[1]
		if secondCandidate.news.ImpactScore >= 85 && news.PassesAudienceRelevanceGate(secondCandidate.news) {
			isSameStory := false
			if valid[0].news.StoryClusterKey != "" && secondCandidate.news.StoryClusterKey != "" {
				sim := embedding.ClusterKeySimilarity(valid[0].news.StoryClusterKey, secondCandidate.news.StoryClusterKey)
				if sim >= 0.5 {
					isSameStory = true
				}
			}
			if !isSameStory {
				newsBytes, err := json.Marshal(secondCandidate.news)
				if err == nil {
					err = cacheAdapter.EnqueueDelayedPost(secondCandidate.hash, secondCandidate.news.Title, secondCandidate.news.Link, string(newsBytes), 30*time.Minute)
					if err == nil {
						logger.Info("🔥 High-Impact secondary news detected! Enqueued for delayed publication (+30m)",
							"title", secondCandidate.news.Title,
							"impact_score", secondCandidate.news.ImpactScore,
							"rank", secondCandidate.rank)
					} else {
						logger.Warn("Failed to enqueue high-impact secondary post", "error", err)
					}
				}
			} else {
				logger.Info("Secondary high-impact candidate is too similar to winning post, skipping delayed queue",
					"winner_key", valid[0].news.StoryClusterKey,
					"candidate_key", secondCandidate.news.StoryClusterKey)
			}
		}
	}
}

// processReadyDelayedPosts checks the delayed posts queue and sends any ready posts to Telegram.
func processReadyDelayedPosts(ctx context.Context, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, supabase *storage.SupabaseClient) {
	ready, err := cacheAdapter.GetReadyDelayedPosts(ctx)
	if err != nil {
		logger.Warn("Failed to check delayed posts queue", "error", err)
		return
	}
	if len(ready) == 0 {
		return
	}

	logger.Info("Found ready delayed posts to publish", "count", len(ready))
	tgPublisher := publisher.NewTelegramPublisher(cfg, cacheAdapter, m)

	for _, dp := range ready {
		var n news.News
		if err := json.Unmarshal([]byte(dp.NewsJSON), &n); err != nil {
			logger.Error("Failed to unmarshal delayed post news JSON", "id", dp.ID, "error", err)
			_ = cacheAdapter.MarkDelayedPostFailed(dp.ID, err.Error())
			continue
		}

		if cacheAdapter.IsAlreadySent(dp.Hash) || (dp.Link != "" && cacheAdapter.IsSourceURLSent(dp.Link)) {
			logger.Info("Delayed post was already sent, marking sent", "title", dp.Title, "hash", dp.Hash)
			_ = cacheAdapter.MarkDelayedPostSent(dp.ID)
			continue
		}

		logger.Info("Publishing delayed high-impact post", "title", dp.Title, "hash", dp.Hash)
		_, success := tgPublisher.Publish(ctx, n, dp.Hash)
		if success {
			_ = cacheAdapter.MarkDelayedPostSent(dp.ID)
			if supabase != nil {
				saveToSupabase(ctx, cacheAdapter, supabase, dp.Hash, n)
			}
			logger.Info("Delayed high-impact post published successfully", "title", dp.Title)
		} else {
			logger.Warn("Failed to publish delayed post", "title", dp.Title)
			_ = cacheAdapter.MarkDelayedPostFailed(dp.ID, "telegram publish failed")
		}
	}
}

// ApplyCategoryAntiFatigue penalizes candidate score by 15 if the last published story was in the same
// heavy category (politics, war, crime). Returns true if penalty was applied.
func ApplyCategoryAntiFatigue(candidate *news.News, lastCategory news.Category) bool {
	candidateCat := news.ValidateCategory(candidate.Category)
	if (lastCategory == news.CategoryPolitics || lastCategory == news.CategoryWar || lastCategory == news.CategoryCrime) && candidateCat == lastCategory {
		candidate.Score -= 15
		return true
	}
	return false
}

// EvaluateDevelopingStory assesses whether a candidate story that matches an existing cluster qualifies as
// an update to an earlier story today.
// Rules:
// 1. Max 2 posts per story cluster per day.
// 2. Second post MUST contain concrete new outcomes/decisions (anchor != empty/insufficient_substance) and AudienceScore >= 6.
// 3. Second post links to the morning post via ReplyToMessageID.
func EvaluateDevelopingStory(candidate news.News, recentStories []storage.SentStoryRecord, clusterThreshold, cosineThreshold float64, shadowMode bool) (approved bool, replyToMsgID int, reason string) {
	clusterCount := 0
	var firstStory *storage.SentStoryRecord
	for _, rec := range recentStories {
		simKey := 0.0
		if candidate.StoryClusterKey != "" && rec.StoryClusterKey != "" {
			simKey = embedding.ClusterKeySimilarity(candidate.StoryClusterKey, rec.StoryClusterKey)
		}
		simCos := 0.0
		if len(candidate.Embedding) > 0 && len(rec.Embedding) > 0 {
			simCos = embedding.CosineSimilarity(candidate.Embedding, rec.Embedding)
		}
		if simKey >= clusterThreshold || (simCos >= cosineThreshold && !shadowMode) {
			clusterCount++
			if firstStory == nil {
				recCopy := rec
				firstStory = &recCopy
			}
		}
	}

	hasSubstance := strings.TrimSpace(candidate.ConcreteAnchor) != "" &&
		!strings.EqualFold(strings.TrimSpace(candidate.ConcreteAnchor), "insufficient_substance")

	if clusterCount == 1 && hasSubstance && candidate.AudienceScore >= 6 {
		if firstStory != nil && firstStory.TelegramMessageID > 0 {
			replyToMsgID = firstStory.TelegramMessageID
		}
		return true, replyToMsgID, ""
	}

	reason = "semantic duplicate"
	if clusterCount >= 2 {
		reason = "daily cluster limit reached (>= 2 posts today)"
	} else if !hasSubstance {
		reason = "developing story update lacks concrete results/anchor"
	} else if candidate.AudienceScore < 6 {
		reason = "developing story update audience score too low (< 6)"
	}
	return false, 0, reason
}

