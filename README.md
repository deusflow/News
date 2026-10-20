# 🇩🇰 Danish News Automation Bot (`dknews`)

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=for-the-badge&logo=go)](https://golang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-Neon_Serverless-4169E1?style=for-the-badge&logo=postgresql)](https://neon.tech)
[![AI Providers](https://img.shields.io/badge/AI-Gemini%20%26%20Groq-8E44AD?style=for-the-badge&logo=google)](https://ai.google.dev)
[![CI/CD](https://img.shields.io/badge/CI%2FCD-GitHub_Actions-2088FF?style=for-the-badge&logo=githubactions)](https://github.com/features/actions)

A scheduled news aggregation, AI summarization, and publishing system built in Go.
It ingests Danish media sources (DR, TV 2, regional outlets, Nyidanmark), pre-filters
stories with non-LLM keyword scoring, deduplicates multi-source coverage, translates
and synthesizes selected stories into Ukrainian via Gemini (Groq fallback), and publishes
them to a Telegram channel. Selected posts also land in Supabase, from which a Hugo
static site is built and deployed to GitHub Pages.

---

## ⏰ How It Runs (GitHub Actions)

| Workflow | Trigger | What it does |
|---|---|---|
| `news.yml` | Cron, 5× daily (06:00–19:00 UTC) | Regular digest: fetch → score → dedup → AI → post to Telegram. Publish limit defaults to 1 post per run (`PUBLISH_LIMIT`). |
| `breaking.yml` | Cron every 20 min (05:00–21:00 UTC), `repository_dispatch: breaking_news`, manual | Emergency mode. Cheap keyword-only detection of acute events (no AI if calm). Fast AI path with a tight request budget. |
| `reddit.yml` | Cron weekly (Sunday) | Weekly digest mode — fetches the last 7 days from Supabase/Neon, AI writes one combined summary post. **Note: the filename is historical; it runs `digest`, not Reddit.** |
| `monthly_calendar.yml` | Cron on the 28th | Monthly calendar post covering the last 35 days. |
| `deploy-site.yml` | After `news.yml` succeeds, on push to `main`, manual | Builds the Hugo website from the Supabase archive and deploys to GitHub Pages. |
| `test-run.yml` | Manual only | Runs on a test channel with PostgreSQL disabled (temporary file cache), so tests never touch production data. |

Optional: `scripts/cloudflare_breaking_worker.js` is a Cloudflare Worker that polls
Danish emergency RSS feeds every ~3 minutes and fires the `breaking_news`
`repository_dispatch` consumed by `breaking.yml`. Deploying it is not required —
`breaking.yml` already has its own frequent cron.

---

## 🔄 Pipeline

```
RSS feeds + Nyidanmark scraper
        │
        ▼
Non-LLM keyword scoring + editorial gates     ← zero AI cost for most stories
        │
        ▼
Deduplication (see below)
        │
        ▼
AI single-worker queue (rate-limited)         ← Gemini, Groq fallback
        │
        ├──► Telegram (channel + admin alerts)
        ├──► Neon PostgreSQL (source of truth)
        └──► Supabase sync queue ──► Hugo site (GitHub Pages)
```

## 🚫 Deduplication Policy

Gates run in order; on a **database outage** behaviour is intentionally split:

| Gate | Identifier type | On DB error | Why |
|---|---|---|---|
| `IsAlreadySent` (hash) | exact | **fail-closed** (skip story) | never double-post |
| `IsSourceURLSent` (link) | exact | **fail-closed** (skip story) | never double-post |
| `IsTitleNearDuplicate` | fuzzy heuristic | **fail-open** (publish) + Warn log | one DB glitch must not silence the whole feed |
| `IsContentDuplicate` | fuzzy heuristic | **fail-open** (publish) + Warn log | same |
| `CheckSemanticDuplicate` | fuzzy heuristic | fail-open | same |

Fuzzy matching includes a normalized-title 5-word key (`title_norm`), a content hash
with significant numbers, and optional semantic dedup:

- **Tier 1 — story cluster key** (Jaccard token overlap across recent stories).
- **Tier 2 — embeddings** (`text-embedding-004` by default), cosine similarity.
  Runs in **shadow mode by default**: it logs what *would* be rejected but does not
  block publication. Embedding calls are capped per run by `MAX_EMBEDDING_REQUESTS`
  (default 12) so they cannot silently burn the weekend Gemini quota; once the budget
  is exhausted, dedup falls back to Tier 1.

## 🧠 AI Quota Protections

- **Single-worker queue** with a configurable inter-request delay
  (`AI_REQUEST_DELAY_SECONDS`, default 7 s) — applies to AI *text* generation.
- **Hard per-run cap** on Gemini text requests (`MAX_GEMINI_REQUESTS`).
- **Provider fallback chain:** Gemini → Groq on failure/rate limits.
- **Publish limit defaults to 1 post per run** — the bot stays cheap by default.
- Admin Telegram alerts (`TELEGRAM_ADMIN_ID`) notify about AI/RSS failures and
  publish-success-without-DB-save events (the latter risks a duplicate next run).

## 🗄️ Storage

- **Neon PostgreSQL** — primary source of truth (`sent_news`, `failed_news` DLQ,
  `delayed_posts` for scheduled high-impact second stories, `fun_fact_cache`,
  Supabase sync queue). Expired rows are cleaned on schedule.
- **Supabase** — secondary archive for the website; the site build reads from it
  (`scripts/generate-pages.sh`), so the bot never needs to commit markdown back to git.
- A file cache (`sent_news.json`) exists as a fallback when PostgreSQL is disabled
  (used by the test workflow).

---

## 🛠️ Tech Stack

- **Language:** Go 1.26+ (standard library focused, minimal dependencies)
- **Database:** Serverless PostgreSQL ([Neon.tech](https://neon.tech)) + [Supabase](https://supabase.com) archive
- **AI:** Google Gemini (`google.golang.org/genai`) with Groq fallback
- **Parsing:** `goquery` (HTML), `gofeed` (RSS/Atom)
- **Site:** Hugo + GitHub Pages
- **Deployment:** GitHub Actions

## 🚀 Getting Started

### Prerequisites

- Go 1.26+
- PostgreSQL connection string (Neon recommended)
- Telegram bot token + channel chat ID (+ optional admin chat ID for alerts)
- Google Gemini API key(s) and a Groq API key

### Configuration

See `.env.example` for a full template. The essential variables:

```env
DATABASE_URL=postgres://user:password@ep-cool-db.neon.tech/main?sslmode=require
USE_POSTGRES=true
TELEGRAM_TOKEN=123456789:ABCdefGHIjklMNOpqrsTUVwxyz
TELEGRAM_CHAT_ID=-1001234567890
TELEGRAM_ADMIN_ID=123456789
GEMINI_API_KEY=AIzaSy...
GROQ_API_KEY=gsk_...
GEMINI_MODEL=gemini-3.5-flash
AI_PROVIDERS=gemini,groq
BOT_MODE=single
```

Commonly tuned (all optional):

| Variable | Default | Meaning |
|---|---|---|
| `PUBLISH_LIMIT` / `MAX_NEWS_LIMIT` | 1 | Posts per run |
| `MAX_GEMINI_REQUESTS` | — | Hard cap on Gemini text calls per run |
| `AI_REQUEST_DELAY_SECONDS` | 7 | Pause between AI text-generation requests |
| `MAX_EMBEDDING_REQUESTS` | 12 | Per-run cap on embedding calls (≤0 = unlimited) |
| `SEMANTIC_DEDUP_ENABLE` / `SEMANTIC_DEDUP_SHADOW_MODE` | true / true | Semantic dedup on; Tier 2 observes only |
| `CACHE_FILE_PATH`, `CACHE_TTL_HOURS` | `sent_news.json`, 48 | File-cache fallback (when PostgreSQL is off) |
| `ENABLE_INLINE_BUTTONS` | true | Inline buttons under Telegram posts |
| `SUPABASE_URL`, `SUPABASE_SERVICE_KEY` | — | Website archive; Supabase auto-enables when both are set |

### Running Locally

```bash
git clone https://github.com/deusflow/News.git
cd News
go mod download

# Run unit tests
go test ./...

# Execute a bot cycle (reads .env / environment)
go run ./cmd/dknews

# Weekly digest / monthly calendar / breaking mode
go run ./cmd/dknews -mode=digest
go run ./cmd/dknews -mode=calendar
go run ./cmd/dknews -mode=breaking
```

---

## 📁 Repository Structure

```
.
├── cmd/
│   ├── dknews/             # Application entry point (mode dispatch)
│   └── testdb/             # Database smoke-test helper
├── internal/
│   ├── ai/                 # Multi-provider AI manager (queue, delay, fallback)
│   │   ├── embedding/      # Gemini embedder + similarity math
│   │   ├── gemini/         # Gemini client
│   │   └── groq/           # Groq client
│   ├── app/                # Orchestrator: gates, publishing, DLQ, delayed posts
│   ├── breaking/           # Emergency (breaking news) mode
│   ├── calendar/           # Monthly calendar mode
│   ├── config/             # Env/YAML configuration loader
│   ├── digest/             # Weekly digest mode
│   ├── logger/             # Structured logging
│   ├── metrics/            # Run metrics + health endpoint data
│   ├── news/               # Scoring, relevance gates, prompts, formatting
│   ├── publisher/          # Telegram media formatting & delivery engine
│   ├── rss/                # RSS/Atom fetching
│   ├── scraper/            # Article extraction (incl. nyidanmark parser)
│   ├── telegram/           # Telegram API client (messages, buttons, YouTube)
│   ├── storage/            # Neon PostgreSQL + Supabase repositories, file cache
│   └── website/            # Hugo markdown generator
├── configs/
│   ├── feeds.yaml          # RSS sources list
│   └── keywords.yaml       # Keyword scoring weights
├── scripts/
│   ├── cloudflare_breaking_worker.js  # Optional edge radar for breaking mode
│   ├── generate-pages.sh   # Supabase → Hugo markdown (site deploy)
│   └── supabase-migration.sql
├── website/                # Hugo site source
└── .github/workflows/      # All CI schedules and the Pages deployment
```

## 🛡️ License

All Rights Reserved — see [`LICENSE`](LICENSE). The code is public for review
and transparency, but copying, modification, and derivative deployments
(including competing bots or sites) are not permitted without written
permission from the owner.
