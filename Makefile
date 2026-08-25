.DEFAULT_GOAL := help

.PHONY: help dev-start dev-stop dev-restart dev-status dev-logs dev-app dev-parser dev-frontend

help:
	@echo "Development commands:"
	@echo "  make dev-start          Start PostgreSQL and Redis in Docker"
	@echo "  make dev-app            Run the Go API on the host (Air when installed)"
	@echo "  make dev-parser         Run the Python gRPC parser on the host"
	@echo "  make dev-frontend       Run the Vite frontend on the host"
	@echo "  make dev-status         Show development infrastructure status"
	@echo "  make dev-logs           Follow development infrastructure logs"
	@echo "  make dev-stop           Stop development infrastructure; keep volumes"

dev-start:
	./scripts/dev.sh start

dev-stop:
	./scripts/dev.sh stop

dev-restart:
	./scripts/dev.sh restart

dev-status:
	./scripts/dev.sh status

dev-logs:
	./scripts/dev.sh logs

dev-app:
	./scripts/dev.sh app

dev-parser:
	./scripts/dev.sh parser

dev-frontend:
	./scripts/dev.sh frontend
