# 🇩🇰 Danish News Automation Bot (`dknews`)

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=for-the-badge&logo=go)](https://golang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-Neon_Serverless-4169E1?style=for-the-badge&logo=postgresql)](https://neon.tech)
[![AI Providers](https://img.shields.io/badge/AI-Gemini%20%26%20Groq-8E44AD?style=for-the-badge&logo=google)](https://ai.google.dev)
[![Edge Radar](https://img.shields.io/badge/Edge-Cloudflare_Worker-F38020?style=for-the-badge&logo=cloudflare)](https://workers.cloudflare.com)
[![CI/CD](https://img.shields.io/badge/CI%2FCD-GitHub_Actions-2088FF?style=for-the-badge&logo=githubactions)](https://github.com/features/actions)
[![License](https://img.shields.io/badge/License-MIT-green.style=for-the-badge)](LICENSE)

An autonomous, event-driven news aggregation, AI summarization, and publishing system built in Go. It ingests Danish media sources (DR, TV2, BT, Ekstra Bladet, Nyidanmark), ranks stories via non-LLM editorial scoring algorithms, performs structural AI translation & synthesis into Ukrainian, and dispatches rich media posts to Telegram and a Supabase-backed web archive.

---

## 🏗️ System Architecture

```mermaid
flowchart TD
    subgraph Trigger & Monitoring
        Cron[GitHub Actions Cron / 5x Daily]
        CFWorker[Cloudflare Edge Worker / 3-min Radar]
    end

    subgraph Data Ingestion & Pre-Filtering
        RSS[RSS Feed Ingestion]
        Scraper[Nyidanmark & Custom Scraper]
        Deduper[3-Tier Pre-AI Deduplication]
        Scorer[Editorial Signal & Cross-Source Jaccard Scoring]
    end

    subgraph AI Pipeline
        Queue[Single-Worker Rate-Limited Job Queue]
        Gemini[Google Gemini 2.5 Flash-Lite Primary]
        Groq[Groq LLaMA Fallback]
    end

    subgraph Storage & Outbound Delivery
        Neon[(Neon PostgreSQL Primary)]
        Telegram[Telegram Channel API]
        SupaSync[(Supabase Web Archive Queue)]
    end

    CFWorker -->|Repository Dispatch| Cron
    Cron --> RSS
    Cron --> Scraper
    RSS & Scraper --> Deduper
    Deduper --> Scorer
    Scorer -->|Top Ranked Candidates| Queue
    Queue --> Gemini
    Gemini -.->|Failover| Groq
    Gemini & Groq --> Telegram
    Telegram -->|Atomic Transaction| Neon
    Neon --> SupaSync
```

---

## 🔥 Key Engineering Highlights

### ⚡ 1. Resilient Rate-Limited AI Pipeline
- **Single-Worker Queue Pattern:** Enforces a strict 7-second inter-request delay (`time.Ticker`) to operate safely within Gemini's 10 RPM Free Tier limits while preventing concurrency thundering herds.
- **Provider Fallback Chain:** Implements graceful degradation across multi-provider LLM managers (`Gemini` $\rightarrow$ `Groq`).
- **Resilience to Cancellation:** Critical post-publishing transactions execute via `context.WithoutCancel()` to guarantee atomic PostgreSQL state updates even if outer workflow contexts expire.

### 🧠 2. 3-Tier Multi-Pass Deduplication Engine
Prevents duplicate publications across heterogeneous news outlets reporting on identical events:
1. **Source Link & FNV-1a Hash Index:** Instant $O(1)$ lookup for exact URL & string matches.
2. **Normalized Title Matching (`title_norm`):** Strips stop-words (Danish/English), numbers, and punctuation; matches on significant 5-word trigrams in PostgreSQL.
3. **Content Hash Index:** Hashes structural article text to detect identical syndications across different domain roots.

### 📊 3. Non-LLM Editorial Signal & Cross-Source Boosting
- **Zero-Cost Pre-Filtering:** Evaluates all candidate stories via keyword weights before invoking LLM APIs, saving up to 80% of daily AI quota.
- **Jaccard Similarity Clustering:** Automatically detects when multiple independent news outlets report on the same story, applying an editorial boost score ($\text{Score} += 15 \times \text{Sources}$) to push high-impact breaking events to the top of the queue.

### 🌐 4. Cloudflare Worker Breaking News Radar
- Edge-deployed JavaScript worker running 3-minute cron checks on Danish emergency feeds (`senestenyt`).
- Employs regex pattern matching for urgent Danish keywords (`LIGE NU`, `PRESSEMØDE`, `HUL I IGENNEM`) and triggers zero-latency `repository_dispatch` webhooks to activate emergency GitHub Action runners.

---

## 🛠️ Tech Stack & Dependencies

- **Language:** Go 1.26+ (Standard Library focused, minimal third-party dependencies)
- **Database:** Serverless PostgreSQL ([Neon.tech](https://neon.tech)) + REST Sync to [Supabase](https://supabase.com)
- **AI Services:** Google Gemini SDK (`generative-ai-go`), Groq REST API
- **Scraping & Parsing:** `goquery` (HTML DOM querying), `gofeed` (RSS/Atom parsing)
- **Deployment:** GitHub Actions Workflows + Cloudflare Workers

---

## 🚀 Getting Started

### Prerequisites

- Go `1.26` or higher installed locally.
- A running PostgreSQL database (or Neon connection string).
- API Keys for Google Gemini, Groq, and Telegram Bot.

### Environment Setup

Create a `.env` file in the root directory:

```env
DATABASE_URL=postgres://user:password@ep-cool-db.neon.tech/main?sslmode=require
USE_POSTGRES=true
TELEGRAM_TOKEN=123456789:ABCdefGHIjklMNOpqrsTUVwxyz
TELEGRAM_CHAT_ID=-1001234567890
GEMINI_API_KEY=AIzaSy...
GROQ_API_KEY=gsk_...
BOT_MODE=single
AI_REQUEST_DELAY_SECONDS=8
```

### Running Locally

```bash
# Clone the repository
git clone https://github.com/deusflow/News.git
cd News

# Install dependencies
go mod download

# Run unit tests
go test ./...

# Execute the news bot cycle
go run ./cmd/dknews
```

---

## 📁 Repository Structure

```
.
├── cmd/
│   └── dknews/            # Application entry point
├── internal/
│   ├── ai/                # Multi-provider LLM manager (Gemini, Groq, Fallback)
│   ├── app/               # Application orchestrator, pipeline & DLQ handlers
│   ├── breaking/          # Emergency news execution workflow
│   ├── config/            # YAML & Env configuration loader
│   ├── news/              # Scoring math, relevance gates, and prompt builders
│   ├── publisher/         # Telegram media formatting & delivery engine
│   ├── rss/               # RSS/Atom feed fetching & parsing
│   ├── scraper/           # HTML article extractors & domain-specific parsers
│   └── storage/           # Neon PostgreSQL & Supabase sync repositories
├── scripts/
│   └── cloudflare_breaking_worker.js # Cloudflare Edge Radar Worker
└── .github/workflows/     # GitHub Actions CI/CD cron schedules
```

---

## 🛡️ License

Distributed under the MIT License. See `LICENSE` for details.
