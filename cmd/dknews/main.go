package main

import (
	"encoding/json"
	"github.com/deusflow/dknews/internal/app"
	"github.com/deusflow/dknews/internal/metrics"
	"log"
	"net/http"
	"os"
)

func main() {
	// Check if we should start HTTP server for monitoring
	if os.Getenv("ENABLE_HTTP_MONITORING") == "true" {
		go startMonitoringServer()
	}

	app.Run()
}

func startMonitoringServer() {
	port := os.Getenv("MONITORING_PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/metrics", metricsHandler)

	log.Printf("Starting monitoring server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("Monitoring server error: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	stats := metrics.Global.GetStats()

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

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	stats := metrics.Global.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
