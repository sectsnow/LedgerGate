GO ?= go
PNPM ?= corepack pnpm

.PHONY: build test dev frontend
frontend:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build
build: frontend
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/ledgergate ./cmd/ledgergate
test:
	$(GO) test ./...
dev:
	$(GO) run ./cmd/ledgergate
