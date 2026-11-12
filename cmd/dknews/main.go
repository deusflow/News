package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/deusflow/News/internal/app"
	"github.com/deusflow/News/internal/breaking"
	"github.com/deusflow/News/internal/calendar"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/digest"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
)

func main() {
	// Check for bot execution mode first
	botMode := os.Getenv("BOT_MODE")
	for i, arg := range os.Args {
		if arg == "-mode=breaking" || arg == "--mode=breaking" {
			botMode = "breaking"
		}
		if arg == "-mode=digest" || arg == "--mode=digest" {
			botMode = "digest"
		}
		if arg == "-mode=calendar" || arg == "--mode=calendar" || arg == "-mode=monthly" || arg == "--mode=monthly" {
			botMode = "calendar"
		}
		if (arg == "-mode" || arg == "--mode") && i+1 < len(os.Args) {
			botMode = os.Args[i+1]
		}
	}

	// Timezone Gating: on scheduled cron runs, verify that current time in Europe/Copenhagen
	// matches the target Danish hour. This automatically handles Summer/Winter time (CET/CEST)
	// shifts with zero manual intervention.
	if !isTargetCopenhagenHour(botMode) {
		return
	}

	// Night Quiet Hours Gating: do not publish regular scheduled news between 22:30 and 07:30 Copenhagen time.
	// Delayed GitHub Actions runner jobs starting at 01:00 or 02:00 AM will gracefully exit without waking subscribers.
	if botMode != "breaking" && app.IsCopenhagenQuietHours(time.Now()) {
		logger.Info("🌙 Quiet hours in Copenhagen (22:30 - 07:30). Skipping Telegram channel publication.",
			"action", "skipping run")
		return
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Config error", "error", err)
		os.Exit(1)
	}

	m := metrics.New()

	// Initialize application
	application, err := app.New(cfg, m)
	if err != nil {
		logger.Error("App init error", "error", err)
		os.Exit(1)
	}

	// Graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	aiMgr := application.GetAIManager()

	if botMode == "breaking" {
		if err := breaking.Run(ctx, cfg, aiMgr, application.GetCacheAdapter(), application.GetSupabase()); err != nil {
			logger.Error("Breaking mode failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if botMode == "digest" {
		if err := digest.RunDigest(ctx, cfg, aiMgr, application.GetSupabase()); err != nil {
			logger.Error("Digest mode failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if botMode == "calendar" || botMode == "monthly" {
		if err := calendar.RunCalendar(ctx, cfg, aiMgr, application.GetSupabase()); err != nil {
			logger.Error("Calendar mode failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// Check if we should start HTTP server for monitoring
	if cfg.Monitoring.EnableHTTPMonitoring {
		go startMonitoringServer(ctx, cfg.Monitoring.Port, m, application)
	}

	application.Run(ctx)

	if summaryFile := os.Getenv("GITHUB_STEP_SUMMARY"); summaryFile != "" {
		writeGitHubSummary(summaryFile, m)
	}
}

func writeGitHubSummary(path string, m *metrics.Metrics) {
	stats := m.GetStats()
	content := fmt.Sprintf(`### 📊 Run Metrics (Danish News Bot)
- **Total News Evaluated:** %v
- **Duplicates Ignored:** %v
- **Successfully Processed by AI:** %v
- **Failed AI Processing:** %v
- **AI API Retries / Rate Limits:** %v
- **Telegram Messages Sent (Published):** %v
`, stats["total_news_processed"], stats["duplicates_filtered"], stats["successful_translations"], stats["failed_translations"], stats["ai_retries"], stats["telegram_messages_sent"])
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		logger.Error("Failed to write GitHub summary", "error", err)
	}
}

// isTargetCopenhagenHour checks if the current time in Europe/Copenhagen matches the intended
// schedule hour when strict hour gating (CHECK_TIMEZONE_HOURS=true) is explicitly enabled.
// By default on GitHub Actions, scheduled cron jobs are经常 delayed by 1–3+ hours by GitHub's
// runner queue, so we must NOT abort delayed scheduled runs based on wall-clock hour unless
// CHECK_TIMEZONE_HOURS=true is explicitly set.
func isTargetCopenhagenHour(botMode string) bool {
	if os.Getenv("CHECK_TIMEZONE_HOURS") != "true" {
		return true // Never drop delayed GitHub Actions cron runs or manual triggers
	}

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		logger.Warn("Failed to load Europe/Copenhagen timezone, bypassing gate", "error", err)
		return true
	}

	nowDK := time.Now().In(loc)
	currentHour := nowDK.Hour()

	if botMode == "digest" {
		// Weekly Sunday digest runs at 20:00 Denmark time
		if currentHour != 20 {
			logger.Info("Timezone gate: current Copenhagen hour does not match Sunday digest target hour (20:00)",
				"current_hour", currentHour,
				"target_hour", 20,
				"action", "skipping run")
			return false
		}
		return true
	}

	if botMode == "calendar" || botMode == "monthly" {
		return true
	}

	if botMode == "breaking" {
		return true
	}

	// Standard news schedule: 08:00, 10:00, 13:00, 17:00, 21:00 Denmark time
	targetHours := []int{8, 10, 13, 17, 21}
	for _, h := range targetHours {
		if currentHour == h {
			logger.Info("Timezone gate: Copenhagen hour matched target schedule",
				"current_hour", currentHour,
				"target_hour", h)
			return true
		}
	}

	logger.Info("Timezone gate: current Copenhagen hour is not a target news hour, skipping run",
		"current_hour", currentHour,
		"target_hours", targetHours,
		"action", "skipping run")
	return false
}

func startMonitoringServer(ctx context.Context, port string, m *metrics.Metrics, a *app.App) {
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		healthHandler(w, r, m, a)
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metricsHandler(w, r, m)
	})
	http.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		reloadHandler(w, r, a)
	})

	server := &http.Server{Addr: ":" + port, Handler: nil}

	go func() {
		logger.Info("Starting monitoring server", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Monitoring server error", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("Shutting down monitoring server")
	if err := server.Shutdown(context.Background()); err != nil {
		logger.Error("Monitoring server shutdown error", "error", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request, m *metrics.Metrics, a *app.App) {
	stats := m.GetStats()
	health := a.CheckHealth(r.Context())

	status := "ok"
	isHealthy, _ := stats["is_healthy"].(bool)
	if !isHealthy {
		status = "error"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	// Check component health
	for k, v := range health {
		if k == "db" && v == "ok (file cache)" {
			continue // Allow db to be "ok (file cache)"
		}
		if v != "ok" && v != "initialized" {
			status = "degraded"
		}
	}

	response := map[string]interface{}{
		"status":     status,
		"last_run":   stats["last_run_time"],
		"last_error": stats["last_error"],
		"components": health,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func metricsHandler(w http.ResponseWriter, _ *http.Request, m *metrics.Metrics) {
	stats := m.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func reloadHandler(w http.ResponseWriter, r *http.Request, a *app.App) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := a.ReloadConfig(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write([]byte("Configuration reloaded successfully"))
}
