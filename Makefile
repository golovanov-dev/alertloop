# AlertLoop developer Makefile.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
ADMIN_DIR := web/admin

.PHONY: build run test test-postgres upgrade-test vet lint fmt tidy release docker clean \
	admin admin-install admin-dev admin-clean

build: ## Build the local binary into bin/ (embeds the current admin UI in internal/adminui/dist)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/alertloop ./cmd/alertloop

admin: ## Build the React admin console into internal/adminui/dist (embedded by the binary)
	cd $(ADMIN_DIR) && npm ci --no-audit --no-fund && npm run build

admin-install: ## Install admin console dependencies
	cd $(ADMIN_DIR) && npm install

admin-dev: ## Run the admin console dev server (proxies /v1 to localhost:8080)
	cd $(ADMIN_DIR) && npm run dev

admin-clean: ## Remove built admin assets
	rm -rf internal/adminui/dist/assets internal/adminui/dist/config.js

all-build: admin build ## Build the admin UI and then the binary that embeds it

run: build ## Run all-in-one mode against a local SQLite database (needs ALERTLOOP_ADMIN_TOKEN)
	./bin/alertloop --config alertloop.example.yaml all

test: ## Run the full test suite (PostgreSQL tests skip without a DSN)
	go test ./...

test-postgres: ## Run the suite against PostgreSQL in a throwaway container
	docker run --rm -d --name alertloop-test-pg \
		-e POSTGRES_USER=alertloop -e POSTGRES_PASSWORD=alertloop -e POSTGRES_DB=alertloop_test \
		-p 55432:5432 postgres:16-alpine
	@until docker exec alertloop-test-pg pg_isready -U alertloop >/dev/null 2>&1; do sleep 1; done
	-ALERTLOOP_TEST_POSTGRES_DSN='postgres://alertloop:alertloop@127.0.0.1:55432/alertloop_test?sslmode=disable' \
		go test -count=1 ./internal/storage/...
	docker rm -f alertloop-test-pg

upgrade-test: ## Install with v0.1.0, upgrade to this tree, check the data survived
	bash scripts/upgrade-test.sh

vet: ## Static analysis
	go vet ./...

lint: ## Run golangci-lint (see .golangci.yml)
	golangci-lint run --timeout=5m

fmt: ## Format all Go source
	gofmt -w internal cmd api

tidy: ## Tidy module dependencies
	go mod tidy

release: ## Cross-compile release artifacts into dist/
	VERSION=$(VERSION) ./scripts/build-release.sh

docker: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) -t alertloop:$(VERSION) .

clean:
	rm -rf bin dist

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
