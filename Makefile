# Makefile для удобного управления проектом

.PHONY: build run test clean lint deps health

# Build the application
build:
	go build -o bin/dknews ./cmd/dknews

# Run the application
run: build
	./bin/dknews

# Run with monitoring enabled
run-with-monitoring: build
	ENABLE_HTTP_MONITORING=true MONITORING_PORT=8080 ./bin/dknews

# Run tests
test:
	go test ./...

# Clean build artifacts
clean:
	rm -rf bin/

# Install dependencies
deps:
	go mod download
	go mod tidy

# Check code quality
lint:
	go vet ./...
	go fmt ./...

# Check health endpoint (when monitoring is enabled)
health:
	curl -s http://localhost:8080/health | jq .

# Check metrics endpoint
metrics:
	curl -s http://localhost:8080/metrics | jq .

# Run with debug logging
debug: build
	DEBUG=true ./bin/dknews

# Single news mode
single: build
	BOT_MODE=single ./bin/dknews

# Development run with monitoring and debug
dev: build
	DEBUG=true ENABLE_HTTP_MONITORING=true MONITORING_PORT=8080 ./bin/dknews
