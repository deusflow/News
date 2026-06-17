# dknews Project Audit Findings

**Date:** 2026-05-11
**Scope:** Full read-only code review of Danish News Bot
**Model:** Claude Opus 4.6

---

## Executive Summary

Comprehensive audit of the dknews Go project identified **14 distinct issues** across bugs, async safety, configuration, and integration gaps. Most are low-to-medium severity; none are critical infrastructure failures. Key categories: observability blind spots, graceful shutdown vulnerabilities, website rendering issues, and deduplication gaps when using file cache.

---

## Issues by Category

### 🔴 BUGS (8 total)

#### BUG-1: Health endpoint `is_healthy` type assertion may always default to false
- **File:** `cmd/dknews/main.go:101`
- **Severity:** Low
- **What:**
  ```go
  isHealthy, _ := stats["is_healthy"].(bool)
  if !isHealthy {
      status = "error"
      w.WriteHeader(http.StatusServiceUnavailable)
  }
  ```
  The type assertion works (field IS `bool` in the map per `metrics.go:119`), but the logic only returns 503 if `is_healthy==false`. This is actually correct behavior.

  **Real issue:** Before the first run completes, `IsHealthy=true` but `last_run_time` is zero-time. Health endpoint returns "ok" before bot has executed anything.

- **Why it matters:** Misleading health status during initialization. Monitoring systems may think the bot is healthy when it hasn't run yet.
- **Impact:** Low — transient condition, resolved after first run.

---

#### BUG-2: `IncrementSuccessfulTranslations` and `IncrementFailedTranslations` are never called
- **File:** `internal/metrics/metrics.go:44-54` (defined but never used)
- **Severity:** Medium
- **What:**
  ```go
  func (m *Metrics) IncrementSuccessfulTranslations() { ... }
  func (m *Metrics) IncrementFailedTranslations() { ... }
  ```
  These methods exist but are not called anywhere in the codebase. The AI processing loop in `internal/news/news.go:309-319` tracks errors locally but never increments metrics.

- **Why it matters:** GitHub Actions summary always displays `0` for both fields. The monitoring dashboard is blind to AI processing success/failure rates.
- **Impact:** Medium — critical observability loss.

---

#### BUG-3: Gemini client default model mismatches config default
- **File:** `internal/ai/gemini/client.go:34` vs `internal/config/config.go:357`
- **Severity:** Low
- **What:**
  ```go
  // config.go default
  cfg.AI.GeminiModel = getEnvOrDefault("GEMINI_MODEL", "gemini-3.5-flash")

  // gemini/client.go fallback
  if modelName == "" {
      modelName = "gemini-2.5-flash"  // FALLBACK DEFAULT
  }
  ```
  If `GeminiModel` is somehow passed as empty string (not normal flow), the client falls back to `2.5-flash` instead of configured `3.5-flash`.

- **Why it matters:** Defense-in-depth gap. Low probability but wrong fallback.
- **Impact:** Low — only triggers if model name explicitly set to `""`.

---

#### BUG-4: Groq JSON mode field is wrong type for OpenAI-compatible API
- **File:** `internal/ai/groq/client.go:42-77`
- **Severity:** Low (dead code)
- **What:**
  ```go
  type groqRequest struct {
      JSONMode bool `json:"json_mode,omitempty"`
  }
  // But never set:
  reqBody := groqRequest{
      Model:       c.model,
      Messages:    messages,
      Temperature: 0.3,
      Stream:      false,
      // JSONMode not set — defaults to false
  }
  ```

  Groq's OpenAI-compatible API expects `response_format: {"type": "json_object"}`, not `json_mode: true`. The field is unused dead code.

- **Why it matters:** If someone enables JSON mode in the future, it won't work as expected.
- **Impact:** Low — currently dead code, but confusing for maintainers.

---

#### BUG-5: DLQ retry loses inline buttons
- **File:** `internal/app/app.go:767-771`
- **Severity:** Medium
- **What:**
  ```go
  if item.ImageURL != "" {
      err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID,
          item.ImageURL, item.MessageText)
  } else {
      _, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token,
          cfg.Telegram.ChatID, item.MessageText)
  }
  ```

  Original `sendOneNews` sends with buttons via `SendPhotoWithButtons`/`SendMessageWithButtons`, but DLQ retry uses bare `SendPhoto`/`SendMessageAllowPreview` — no inline buttons.

- **Why it matters:** User experience degradation. "Read original" and "Watch video" buttons are missing from retried posts.
- **Impact:** Medium — user-facing feature loss.

---

#### BUG-6: `EnqueueSupabaseSync` ON CONFLICT clause missing conflict target
- **File:** `internal/storage/postgres.go:567-573`
- **Severity:** Low
- **What:**
  ```sql
  INSERT INTO supabase_sync_queue (sent_news_hash, payload, attempts, created_at)
  VALUES ($1, $2, 0, NOW())
  ON CONFLICT DO NOTHING  -- ❌ Missing conflict target!
  ```

  Table `supabase_sync_queue` has no unique constraint on `sent_news_hash` (only an index). The `ON CONFLICT` matches only the `id` serial PK.

- **Why it matters:** Same hash can be enqueued multiple times. On successive failures, duplicate sync queue entries accumulate.
- **Impact:** Low — redundant Supabase writes, but eventual consistency handles duplicates.

---

#### BUG-7: Website YAML front matter title double-escaping
- **File:** `internal/website/generator.go:120`
- **Severity:** Medium
- **What:**
  ```go
  sb.WriteString(fmt.Sprintf("title: %q\n", escapeYAML(post.Title)))
  ```

  `escapeYAML` handles quotes and backslashes, then `%q` applies Go-style quoting on top. A title like `He said "hello"` becomes:
  ```yaml
  title: "He said \\\"hello\\\""
  ```

- **Why it matters:** Titles with quotes/backslashes render incorrectly in Hugo. YAML parser may interpret escaped quotes as literal `\"`.
- **Impact:** Medium — website rendering broken for edge-case titles.

---

#### BUG-8: DLQ retried items get `category="DLQ"` in database
- **File:** `internal/app/app.go:788`
- **Severity:** Low
- **What:**
  ```go
  hash := adapter.GenerateNewsHash(item.Title, item.Link)
  _ = adapter.MarkAsSent(hash, item.Title, item.Link, "DLQ", "DLQ")
  ```

  The category is hardcoded to `"DLQ"` instead of preserving the original news category.

- **Why it matters:** Analytics and statistics are polluted. Category counts in `sent_news` table include "DLQ" retries.
- **Impact:** Low — analytics only, doesn't affect publishing.

---

### 🟡 ASYNC & CONCURRENCY ISSUES (4 total)

#### ASYNC-1: AI Manager goroutine leak on shutdown
- **File:** `internal/ai/manager.go:84, 150`
- **Severity:** Low
- **What:**
  ```go
  go mgr.worker(ctx)  // Line 84 — worker runs until ctx.Done()

  func (m *Manager) Generate(ctx context.Context, title, content, prompt string) (*Response, error) {
      resultCh = make(chan aiResult, 1)
      // ... enqueue job ...
      select {
      case res := <-resultCh:
          return res.resp, res.err
      case <-ctx.Done():
          return nil, ctx.Err()
      }
  }
  ```

  When `Close()` cancels the manager's context, pending jobs in the 50-item buffer never get drained. Callers blocked on `<-resultCh` hang until their per-item context (30s) times out.

- **Why it matters:** Graceful shutdown delayed up to 30 seconds per pending AI job.
- **Impact:** Low — affects shutdown speed, not correctness.

---

#### ASYNC-2: `processFailedMessages` ignores shutdown context
- **File:** `internal/app/app.go:294`
- **Severity:** Low
- **What:**
  ```go
  func (a *App) Run(ctx context.Context) {
      // ...
      processFailedMessages(a.cacheAdapter, a.cfg, a.metrics)  // No context passed
  }

  func processFailedMessages(adapter CacheAdapter, cfg *config.Config, m *metrics.Metrics) {
      // ...
      _, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token,
          cfg.Telegram.ChatID, item.MessageText)  // Uses context.Background()
  }
  ```

- **Why it matters:** Telegram retries during DLQ processing won't be cancelled on SIGTERM.
- **Impact:** Low — DLQ processing runs to completion regardless of shutdown signal.

---

#### ASYNC-3: `ArchiveOldNews` and `fetchNews` don't use context
- **File:** `internal/storage/supabase.go:555, 581`
- **Severity:** Low
- **What:**
  ```go
  // ArchiveOldNews (line 555)
  req, err := http.NewRequest("PATCH", reqURL, bytes.NewBuffer(body))  // No context!

  // fetchNews (line 581)
  req, err := http.NewRequest("GET", reqURL, nil)  // No context!
  ```

  Both create bare `http.Request` without context. No cancellation support, no retry logic, no graceful shutdown awareness.

- **Why it matters:** These HTTP calls block up to 30s HTTP timeout and ignore SIGTERM.
- **Impact:** Low — affects website archive operations, not core publishing.

---

#### ASYNC-4: Race condition in `ReloadConfig` (hot reload)
- **File:** `internal/app/app.go:356-381`
- **Severity:** Medium
- **What:**
  ```go
  func (a *App) ReloadConfig() error {
      feeds, err := rss.LoadFeeds(a.cfg.RSS.FeedsConfigPath)
      if rssFetcher, ok := a.fetcher.(*RSSFetcher); ok {
          rssFetcher.feeds = feeds  // ❌ Direct mutation without sync!
      }
      // ...
      filterProcessor.keywords = keywords  // ❌ No synchronization
  }
  ```

  The `/reload` endpoint can mutate `fetcher.feeds` and `processor.keywords` while `Run()` is executing and reading from these same fields.

- **Why it matters:** Concurrent read/write without synchronization. `go test -race` would flag this.
- **Impact:** Medium — Data race, though low probability in practice (single cycle + rare reload).

---

### 🟠 CONFIGURATION ISSUES (3 total)

#### CONFIG-1: FileCacheAdapter disables 4 of 5 dedup methods
- **File:** `internal/app/cache_adapter.go:50-63`
- **Severity:** Medium
- **What:**
  ```go
  type FileCacheAdapter struct {
      cache *storage.FileCache
  }

  func (f *FileCacheAdapter) IsSourceURLSent(sourceURL string) bool {
      return false  // ❌ Always false
  }

  func (f *FileCacheAdapter) IsContentDuplicate(content string) (bool, string) {
      return false, ""  // ❌ Always false
  }

  func (f *FileCacheAdapter) IsTitleNearDuplicate(title string) (bool, string) {
      return false, ""  // ❌ Always false
  }
  ```

  When `USE_POSTGRES=false`, only hash-based dedup works. All other methods are stubs.

- **Why it matters:** Without PostgreSQL, the bot has almost zero dedup protection and will republish the same story from different sources multiple times.
- **Impact:** Medium — significant data quality issue when file cache is used.

---

#### CONFIG-2: Default HTTP mux exposure risk
- **File:** `cmd/dknews/main.go:70-80`
- **Severity:** Low
- **What:**
  ```go
  http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { ... })
  http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) { ... })
  http.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) { ... })

  server := &http.Server{Addr: ":" + port, Handler: nil}  // Uses default mux
  ```

  Routes are registered on the global `http.DefaultServeMux`. If any imported package also calls `http.Handle` or `http.HandleFunc`, their routes get exposed on the monitoring port.

- **Why it matters:** Unintended route exposure. Third-party libraries' debug endpoints might leak through.
- **Impact:** Low — security posture, probably low risk in practice.

---

#### CONFIG-3: `PHOTO_TEXT_LIMIT` env var is not configurable
- **File:** `internal/config/config.go:322`
- **Severity:** Low
- **What:**
  ```go
  Posting: PostingConfig{
      // ...
      PhotoTextLimit: 1024,  // ❌ Hardcoded, no env override
  }
  ```

  All other `PostingConfig` values have env overrides (`PHOTO_CAPTION_MAX_RUNES`, `PHOTO_MIN_PER_LANG_RUNES`, etc.), but `PhotoTextLimit` is hardcoded.

- **Why it matters:** Cannot tune the photo caption limit without code change.
- **Impact:** Low — mostly theoretical concern.

---

### 🔗 INTEGRATION GAPS (3 total)

#### INTEG-1: News struct field `Summary` is populated but never used
- **File:** `internal/news/news.go:39`, `internal/news/format.go`
- **Severity:** Info (wasted tokens)
- **What:**
  ```go
  type News struct {
      Summary          string   `json:"summary"`  // Populated by AI
      SummaryDanish    string   `json:"danish"`
      SummaryUkrainian string   `json:"ukrainian"`
  }

  // In FormatNewsWithImage and FormatCaptionForPhoto:
  // Only SummaryDanish and SummaryUkrainian are used.
  // Summary is never referenced.
  ```

- **Why it matters:** The AI generates a `summary` field that's never displayed. Wasted tokens in every request.
- **Impact:** Info — minor inefficiency.

---

#### INTEG-2: `NewsPost.ContentUkrainian` and `ContentDanish` are never populated
- **File:** `internal/website/generator.go:19-20`, `internal/app/app.go:633-649`
- **Severity:** Medium
- **What:**
  ```go
  // generator.go expects:
  type NewsPost struct {
      ContentUkrainian string
      ContentDanish    string
  }

  // But app.go never sets them:
  post := website.NewsPost{
      Title:            n.Title,
      Content:          n.Content,  // Raw scraped content
      SummaryUkrainian: n.SummaryUkrainian,  // AI-translated
      SummaryDanish:    n.SummaryDanish,     // AI-translated
      // ContentUkrainian and ContentDanish not set
  }
  ```

  The generator falls back to `post.SummaryUkrainian` or `post.Content` (raw scraped, not translated).

- **Why it matters:** The Hugo website shows raw English/Danish scraped text under "🇺🇦 Українською" section instead of AI-translated Ukrainian summary.
- **Impact:** Medium — website shows wrong content language for Ukrainian section.

---

#### INTEG-3: Supabase `NewsArchive.Tags` serialization may fail
- **File:** `internal/storage/supabase.go:151`
- **Severity:** Low (depends on Supabase schema)
- **What:**
  ```go
  type NewsArchive struct {
      Tags []string `json:"tags,omitempty"`
  }

  // Serialized as JSON array: ["tag1", "tag2"]
  // But if Supabase table has `tags TEXT[]` (array type),
  // it may reject JSON array format
  ```

- **Why it matters:** Depends on whether Supabase `news_archive` table has `tags` as `jsonb` or `text[]`.
- **Impact:** Low — schema-dependent, may cause silent tag-save failures.

---

## Summary Table

| ID | Category | Severity | Issue | File | Impact |
|---|---|---|---|---|---|
| BUG-1 | Bug | Low | Health status initialization | main.go:101 | Transient condition |
| BUG-2 | Bug | **Medium** | Metrics never incremented | metrics.go | Blind monitoring |
| BUG-3 | Bug | Low | Model name mismatch | gemini.go:34 | Latent gap |
| BUG-4 | Bug | Low | Groq JSON mode dead code | groq.go:47 | Dead code |
| BUG-5 | Bug | **Medium** | DLQ retry loses buttons | app.go:771 | UX degradation |
| BUG-6 | Bug | Low | Sync queue duplicates | postgres.go:570 | Redundant writes |
| BUG-7 | Bug | **Medium** | YAML title escaping | generator.go:120 | Website rendering |
| BUG-8 | Bug | Low | DLQ category="DLQ" | app.go:788 | Analytics pollution |
| ASYNC-1 | Async | Low | Manager shutdown leak | manager.go:84 | Slow shutdown |
| ASYNC-2 | Async | Low | DLQ ignores context | app.go:294 | Untrackable shutdown |
| ASYNC-3 | Async | Low | Supabase no context | supabase.go:555 | Untrackable shutdown |
| ASYNC-4 | Async | **Medium** | Reload race condition | app.go:356 | Data race |
| CONFIG-1 | Config | **Medium** | FileCache dedup stubs | cache_adapter.go:50 | Duplicate posts |
| CONFIG-2 | Config | Low | Default mux exposure | main.go:80 | Security posture |
| CONFIG-3 | Config | Low | PhotoTextLimit not tunable | config.go:322 | Limited flexibility |
| INTEG-1 | Integration | Info | Unused `Summary` field | news.go:39 | Wasted tokens |
| INTEG-2 | Integration | **Medium** | Website content not translated | app.go:637 | Wrong content |
| INTEG-3 | Integration | Low | Tags serialization schema-dependent | supabase.go:151 | Possible failures |

---

## Recommendations by Priority

### High Priority (Fix Soon)
1. **BUG-2:** Call `opts.Metrics.IncrementSuccessfulTranslations()` and `IncrementFailedTranslations()` in `news.go` AI processing loop.
2. **BUG-5:** Preserve and pass inline buttons through DLQ retry path.
3. **BUG-7:** Fix YAML escaping — remove `%q` when `escapeYAML` already handles quoting.
4. **INTEG-2:** Populate `post.ContentUkrainian` and `post.ContentDanish` with AI-translated summaries.
5. **CONFIG-1:** Implement full dedup for FileCache, or require PostgreSQL.

### Medium Priority (Improve)
6. **ASYNC-4:** Add mutex for hot-reload config changes to prevent data races.
7. **BUG-6:** Add unique constraint on `supabase_sync_queue(sent_news_hash)` or fix ON CONFLICT target.
8. **ASYNC-1, ASYNC-2, ASYNC-3:** Pass context through shutdown paths for proper cancellation.

### Low Priority (Tech Debt)
9. **BUG-3, BUG-4, BUG-8:** Clean up dead code and consistency gaps.
10. **CONFIG-2:** Use custom mux instead of default mux.
11. **CONFIG-3:** Add env override for `PHOTO_TEXT_LIMIT`.
12. **INTEG-1:** Remove unused `Summary` field or use it in formatting.

---

## Testing Recommendations

```bash
# Run race detector
go test -race ./...

# Check for lint issues
golangci-lint run ./...

# Verify config loading
go run cmd/dknews/main.go --help  # if added

# Manual test: hot reload
curl -X POST http://localhost:8080/reload

# Manual test: health check
curl http://localhost:8080/health | jq .is_healthy
```

---

## Notes

- **No critical/infrastructure-breaking bugs found.** The bot architecture is sound.
- **Observability gap (BUG-2) is the highest-impact issue** — metrics provide zero visibility into AI processing quality.
- **FileCache mode (CONFIG-1) significantly degrades dedup** — strongly recommend PostgreSQL in production.
- **Website content (INTEG-2) shows untranslated text** — confusing user experience.
- **Graceful shutdown paths lack context propagation** — minor issue for a single-shot bot, but bad pattern.

---

**Audit completed:** No major architectural issues, all findings are addressable with localized code changes.
