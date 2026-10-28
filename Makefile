.PHONY: help run dev build test test-e2e verify-r2 docker-up docker-down docker-logs docker-ps db-migrate db-psql sqlc clean

# Default target
.DEFAULT_GOAL := help

# Colors for terminal output
CYAN  := \033[36m
GREEN := \033[32m
RESET := \033[0m

## ----------------------------------------------------------------------
## 🛠️ BITMILL MEDIA PLATFORM AUTOMATION MAKEFILE
## ----------------------------------------------------------------------

help: ## Show this help message
	@echo ""
	@echo "$(CYAN)Usage:$(RESET) make [target]"
	@echo ""
	@echo "$(CYAN)Available targets:$(RESET)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(GREEN)%-18s$(RESET) %s\n", $$1, $$2}'
	@echo ""

## 🚀 Application & Development
run: ## Run the Bitmill API server (alias for dev)
	cd go-streaming && go run cmd/server/main.go

dev: run ## Run the Bitmill API server in development mode

build: ## Compile Go application binaries into bin/
	cd go-streaming && go build -o bin/server cmd/server/main.go

test: ## Execute all Go unit tests with race detection and coverage
	cd go-streaming && go test -v -race ./internal/...

test-e2e: ## Execute live end-to-end integration test with real Cloudflare R2
	cd go-streaming && go run cmd/verify_multipart_r2/main.go

verify-r2: test-e2e ## Alias for test-e2e

## 🐳 Infrastructure & Docker
docker-up: ## Start PostgreSQL and RabbitMQ containers in background
	docker compose up -d

docker-down: ## Stop all background Docker containers
	docker compose down

docker-logs: ## Tail logs from all Docker containers
	docker compose logs -f

docker-ps: ## View status of running Docker containers
	docker compose ps

## 🗄️ Database Management
db-migrate: ## Apply database migrations to the running PostgreSQL container
	@echo "Applying base schema..."
	docker exec -i bitmill-postgres psql -U bitmill_user -d bitmill_media < scripts/init-db.sql
	@echo "Applying v2 multipart migration..."
	docker exec -i bitmill-postgres psql -U bitmill_user -d bitmill_media < scripts/migrate-v2-multipart.sql
	@echo "✅ Database migrations applied successfully!"

db-psql: ## Open an interactive psql session in the PostgreSQL container
	docker exec -it bitmill-postgres psql -U bitmill_user -d bitmill_media

## ⚙️ Code Generation
sqlc: ## Generate type-safe Go database code using sqlc
	cd go-streaming && go run github.com/sqlc-dev/sqlc/cmd/sqlc generate

## 🧹 Maintenance
clean: ## Remove build binaries and temporary artifacts
	rm -rf go-streaming/bin/
	rm -rf go-streaming/tmp/
	@echo "✅ Cleaned build artifacts."
