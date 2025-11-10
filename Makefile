.PHONY: help install dev up down clean build run test migrate seed fresh keys lint fmt

# Variables
SERVER_BINARY=bin/server
MIGRATE_BINARY=bin/migrate
DOCKER_COMPOSE=docker-compose -f docker-compose.dev.yml

# Default target
help: ## Show this help message
	@echo "Heimdall - Authentication Service"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

install: ## Install Go dependencies
	@echo "📦 Installing dependencies..."
	@go mod download
	@go mod tidy
	@echo "✅ Dependencies installed"

dev: ## Start development environment (Docker services)
	@echo "🚀 Starting development environment..."
	@$(DOCKER_COMPOSE) up -d
	@echo "✅ Development environment started"
	@echo ""
	@echo "Services:"
	@echo "  PostgreSQL:  localhost:5432"
	@echo "  Redis:       localhost:6379"
	@echo "  FusionAuth:  http://localhost:9011"
	@echo ""
	@echo "Run 'make logs' to view logs"

up: dev ## Alias for dev

down: ## Stop development environment
	@echo "🛑 Stopping development environment..."
	@$(DOCKER_COMPOSE) down
	@echo "✅ Development environment stopped"

clean: down ## Stop environment and remove volumes
	@echo "🧹 Cleaning up..."
	@$(DOCKER_COMPOSE) down -v
	@rm -rf bin/
	@echo "✅ Cleanup complete"

logs: ## Show Docker container logs
	@$(DOCKER_COMPOSE) logs -f

build: ## Build all binaries
	@echo "🔨 Building binaries..."
	@mkdir -p bin
	@go build -o $(SERVER_BINARY) ./cmd/server
	@go build -o $(MIGRATE_BINARY) ./cmd/migrate
	@echo "✅ Build complete"

run: ## Run the Heimdall server
	@echo "🚀 Starting Heimdall server..."
	@go run cmd/server/main.go

migrate: ## Run database migrations
	@echo "🔄 Running migrations..."
	@go run cmd/migrate/main.go up

seed: ## Seed database with default data
	@echo "🌱 Seeding database..."
	@go run cmd/migrate/main.go seed

fresh: ## Run migrations and seed database
	@echo "🔄 Running fresh migration..."
	@go run cmd/migrate/main.go fresh

keys: ## Generate JWT RSA keys
	@echo "🔑 Generating JWT keys..."
	@mkdir -p keys
	@openssl genrsa -out keys/private.pem 2048
	@openssl rsa -in keys/private.pem -pubout -out keys/public.pem
	@echo "✅ Keys generated in keys/ directory"

test: ## Run all tests
	@echo "🧪 Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...
	@echo "✅ Tests complete"

test-unit: ## Run unit tests only
	@echo "🧪 Running unit tests..."
	@go test -v -race ./internal/auth ./internal/service ./internal/middleware
	@echo "✅ Unit tests complete"

test-integration: ## Run integration tests only
	@echo "🧪 Running integration tests..."
	@./test/run-integration-tests.sh
	@echo "✅ Integration tests complete"

test-auth: ## Run authentication integration tests only
	@echo "🧪 Running authentication tests..."
	@export HEIMDALL_API_URL=http://localhost:8080 && \
		go test -v ./test/integration -run TestUser -timeout 5m
	@echo "✅ Authentication tests complete"

test-coverage: test ## Run tests with coverage report
	@go tool cover -html=coverage.out -o coverage.html
	@echo "📊 Coverage report generated: coverage.html"
	@echo "📈 Overall coverage:"
	@go tool cover -func=coverage.out | grep total | awk '{print $$3}'

test-watch: ## Run tests in watch mode (requires entr)
	@echo "👀 Watching for changes..."
	@find . -name '*.go' | entr -c make test

test-db-setup: ## Setup test database
	@echo "🗄️  Setting up test database..."
	@psql -U postgres -c "DROP DATABASE IF EXISTS heimdall_test;" || true
	@psql -U postgres -c "CREATE DATABASE heimdall_test;"
	@psql -U postgres -c "GRANT ALL PRIVILEGES ON DATABASE heimdall_test TO heimdall;"
	@echo "✅ Test database ready"

test-clean: ## Clean test artifacts
	@echo "🧹 Cleaning test artifacts..."
	@rm -f coverage.out coverage.html
	@echo "✅ Test artifacts cleaned"

lint: ## Run linter
	@echo "🔍 Running linter..."
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "⚠️  golangci-lint not installed. Install it from https://golangci-lint.run/usage/install/"; \
	fi

fmt: ## Format code
	@echo "✨ Formatting code..."
	@go fmt ./...
	@echo "✅ Code formatted"

setup: install keys dev migrate seed ## Complete setup (install, keys, dev env, migrate, seed)
	@echo ""
	@echo "✅ Setup complete! Heimdall is ready to use."
	@echo ""
	@echo "Next steps:"
	@echo "  1. Copy .env.example to .env and configure"
	@echo "  2. Run 'make run' to start the server"
	@echo "  3. Visit http://localhost:8080/health to verify"

env: ## Create .env file from .env.example
	@if [ ! -f .env ]; then \
		echo "📝 Creating .env file..."; \
		cp .env.example .env; \
		echo "✅ .env file created. Please update it with your configuration."; \
	else \
		echo "⚠️  .env file already exists"; \
	fi

status: ## Show status of Docker services
	@$(DOCKER_COMPOSE) ps

restart: down up ## Restart development environment

.DEFAULT_GOAL := help
