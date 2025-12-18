package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/deusflow/News/internal/app"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/metrics"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	m := metrics.New()

	// Initialize application
	application, err := app.New(cfg, m)
	if err != nil {
		log.Fatalf("App init error: %v", err)
	}

	// Check if we should start HTTP server for monitoring
	if cfg.EnableHTTPMonitoring {
		go startMonitoringServer(cfg.MonitoringPort, m, application)
	}

	// Graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application.Run(ctx)
}

func startMonitoringServer(port string, m *metrics.Metrics, a *app.App) {
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

	log.Printf("Starting monitoring server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("Monitoring server error: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request, m *metrics.Metrics, a *app.App) {
	stats := m.GetStats()
	health := a.CheckHealth(r.Context())

	status := "ok"
	if !stats["is_healthy"].(bool) {
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
