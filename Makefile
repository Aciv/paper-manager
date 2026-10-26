.DEFAULT_GOAL := help

.PHONY: help dev dev-start dev-stop dev-restart dev-status dev-logs dev-infra-logs dev-app dev-parser dev-frontend

help:
	@echo "Development commands:"
	@echo "  make dev                Start infrastructure + API + parser + frontend"
	@echo "  make dev-start          Start PostgreSQL and Redis in Docker"
	@echo "  make dev-app            Run the Go API on the host (Air when installed)"
	@echo "  make dev-parser         Run the Python gRPC parser on the host"
	@echo "  make dev-frontend       Run the Vite frontend on the host"
	@echo "  make dev-status         Show development infrastructure status"
	@echo "  make dev-logs           Follow source logs (optionally SERVICE=api|parser|web)"
	@echo "  make dev-infra-logs     Follow PostgreSQL and Redis logs"
	@echo "  make dev-stop           Stop development infrastructure; keep volumes"

dev:
	./scripts/dev/dev.sh all

dev-start:
	./scripts/dev/dev.sh start

dev-stop:
	./scripts/dev/dev.sh stop

dev-restart:
	./scripts/dev/dev.sh restart

dev-status:
	./scripts/dev/dev.sh status

dev-logs:
	./scripts/dev/dev.sh source-logs "$(SERVICE)"

dev-infra-logs:
	./scripts/dev/dev.sh logs

dev-app:
	./scripts/dev/dev.sh app

dev-parser:
	./scripts/dev/dev.sh parser

dev-frontend:
	./scripts/dev/dev.sh frontend
