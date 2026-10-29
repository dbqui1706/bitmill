.PHONY: help run dev build worker build-worker test test-e2e verify-r2 verify-worker worktree-create worktree-merge worktree-clean docker-up docker-down docker-logs docker-ps db-psql migrate-up migrate-down migrate-status migrate-create db-migrate sqlc clean

# Default target
.DEFAULT_GOAL := help

# Colors for terminal output
CYAN  := \033[36m
GREEN := \033[32m
RESET := \033[0m

# Tool paths (fallback to GOPATH if not in global PATH)
GOOSE := $(shell which goose 2>/dev/null || echo "$$(go env GOPATH)/bin/goose")
SQLC  := $(shell which sqlc 2>/dev/null || echo "$$(go env GOPATH)/bin/sqlc")

# Database connection
DB_DSN ?= postgres://bitmill_user:bitmill_password@localhost:5432/bitmill_media?sslmode=disable

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

worker: ## Run the Bitmill transcoding worker daemon
	cd go-streaming && go run cmd/worker/main.go

build: ## Compile Go application binaries into bin/
	cd go-streaming && go build -o bin/server cmd/server/main.go && go build -o bin/worker cmd/worker/main.go

build-worker: ## Compile transcoding worker binary into bin/worker
	cd go-streaming && go build -o bin/worker cmd/worker/main.go

test: ## Execute all Go unit tests with race detection and coverage
	cd go-streaming && go test -v -race ./internal/...

test-e2e: ## Execute live end-to-end integration test with real Cloudflare R2
	cd go-streaming && go run cmd/verify_multipart_r2/main.go

verify-r2: test-e2e ## Alias for test-e2e

verify-worker: ## Execute live end-to-end integration test for RabbitMQ worker & FFmpeg transcoding
	cd go-streaming && go run cmd/verify_transcoder_worker/main.go

## 🌿 Git Worktree Isolation Workflow
worktree-create: ## Create an isolated worktree (usage: make worktree-create NAME=milestone-5)
	@if [ -z "$(NAME)" ]; then echo "Error: Please specify NAME, e.g., make worktree-create NAME=milestone-5"; exit 1; fi
	@mkdir -p .worktrees
	git worktree add -b feat/$(NAME) .worktrees/$(NAME) main
	@if [ -f .env ]; then ln -sf ../../.env .worktrees/$(NAME)/.env; fi
	@echo "$(GREEN)✅ Worktree created at .worktrees/$(NAME) on branch feat/$(NAME)$(RESET)"

worktree-merge: ## Merge worktree feature branch back into main (usage: make worktree-merge NAME=milestone-5)
	@if [ -z "$(NAME)" ]; then echo "Error: Please specify NAME, e.g., make worktree-merge NAME=milestone-5"; exit 1; fi
	git checkout main
	git merge --no-ff feat/$(NAME) -m "feat($(NAME)): merge $(NAME) from worktree"
	@echo "$(GREEN)✅ Merged feat/$(NAME) into main$(RESET)"

worktree-clean: ## Remove worktree directory and branch (usage: make worktree-clean NAME=milestone-5)
	@if [ -z "$(NAME)" ]; then echo "Error: Please specify NAME, e.g., make worktree-clean NAME=milestone-5"; exit 1; fi
	git worktree remove .worktrees/$(NAME)
	git branch -d feat/$(NAME)
	@echo "$(GREEN)🧹 Cleaned up worktree .worktrees/$(NAME)$(RESET)"

## 🐳 Infrastructure, Docker & Observability
docker-up: ## Start all infrastructure containers (Postgres, RabbitMQ, Prometheus, Loki, Promtail, Grafana)
	docker compose up -d

docker-down: ## Stop all background Docker containers
	docker compose down

docker-logs: ## Tail logs from all Docker containers
	docker compose logs -f

docker-ps: ## View status of running Docker containers
	docker compose ps

obs-up: ## Start Prometheus, Loki, Promtail, and Grafana monitoring stack
	docker compose up -d prometheus loki promtail grafana

obs-down: ## Stop monitoring stack containers
	docker compose stop prometheus loki promtail grafana

## 🗄️ Database Management & Goose Migrations
migrate-up: ## Run all pending database migrations via Goose
	$(GOOSE) -dir migrations postgres "$(DB_DSN)" up

migrate-down: ## Roll back the most recent database migration via Goose
	$(GOOSE) -dir migrations postgres "$(DB_DSN)" down

migrate-status: ## Check current database migration status via Goose
	$(GOOSE) -dir migrations postgres "$(DB_DSN)" status

migrate-create: ## Create a new SQL migration file (usage: make migrate-create name=add_users)
	@if [ -z "$(name)" ]; then echo "Error: Please specify migration name, e.g., make migrate-create name=add_users"; exit 1; fi
	$(GOOSE) -dir migrations create $(name) sql

db-migrate: migrate-up ## Alias for migrate-up

db-psql: ## Open an interactive psql session in the PostgreSQL container
	docker exec -it bitmill-postgres psql -U bitmill_user -d bitmill_media

## ⚙️ Code Generation
sqlc: ## Generate type-safe Go database code from migrations using sqlc
	cd go-streaming && $(SQLC) generate

## 🧹 Maintenance
clean: ## Remove build binaries and temporary artifacts
	rm -rf go-streaming/bin/
	rm -rf go-streaming/tmp/
	@echo "✅ Cleaned build artifacts."
