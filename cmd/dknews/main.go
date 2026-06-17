package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/deusflow/News/internal/app"
	"github.com/deusflow/News/internal/breaking"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/reddit"
)

func main() {
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

	// Check for Reddit mode
	botMode := os.Getenv("BOT_MODE")
	for i, arg := range os.Args {
		if arg == "-mode=reddit" || arg == "--mode=reddit" {
			botMode = "reddit"
		}
		if arg == "-mode=breaking" || arg == "--mode=breaking" {
			botMode = "breaking"
		}
		if (arg == "-mode" || arg == "--mode") && i+1 < len(os.Args) {
			botMode = os.Args[i+1]
		}
	}

	aiMgr := application.GetAIManager()

	if botMode == "reddit" {
		if err := reddit.Run(ctx, cfg, aiMgr); err != nil {
			logger.Error("Reddit mode failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if botMode == "breaking" {
		if err := breaking.Run(ctx, cfg, aiMgr); err != nil {
			logger.Error("Breaking mode failed", "error", err)
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
		if v != "ok" && v != "initialized" && k != "db" { // Allow db to be "ok (file cache)"
			if k == "db" && v == "ok (file cache)" {
				continue
			}
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

func metricsHandler(w http.ResponseWriter, r *http.Request, m *metrics.Metrics) {
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
