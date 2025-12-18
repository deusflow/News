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

	// Check if we should start HTTP server for monitoring
	if cfg.EnableHTTPMonitoring {
		go startMonitoringServer(cfg.MonitoringPort, m)
	}

	// Graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app.Run(ctx, cfg, m)
}

func startMonitoringServer(port string, m *metrics.Metrics) {
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		healthHandler(w, r, m)
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metricsHandler(w, r, m)
	})

	log.Printf("Starting monitoring server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("Monitoring server error: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request, m *metrics.Metrics) {
	stats := m.GetStats()

	status := "ok"
	if !stats["is_healthy"].(bool) {
		status = "error"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	response := map[string]interface{}{
		"status":     status,
		"last_run":   stats["last_run_time"],
		"last_error": stats["last_error"],
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func metricsHandler(w http.ResponseWriter, r *http.Request, m *metrics.Metrics) {
	stats := m.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
