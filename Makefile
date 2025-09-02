# Makefile для удобного управления проектом

# Собрать проект
build:
	go build -o bin/dknews cmd/dknews/main.go

# Запустить проект с автозагрузкой .env
run:
	./run.sh

# Запустить с переменными окружения из .env файла (альтернативный способ)
run-env:
	@if [ -f .env ]; then \
		export $$(cat .env | grep -v '^#' | xargs) && go run cmd/dknews/main.go; \
	else \
		echo "Файл .env не найден. Создайте его из .env.example"; \
	fi

# Установить зависимости
deps:
	go mod download
	go mod tidy

# Проверить код
check:
	go vet ./...
	go fmt ./...

# Очистить
clean:
	rm -rf bin/

# Тест (сухой прогон без отправки в Telegram)
test:
	@echo "Запуск в тестовом режиме..."
	@export TELEGRAM_TOKEN=test && export TELEGRAM_CHAT_ID=test && go run cmd/dknews/main.go

.PHONY: build run run-env deps check clean test
