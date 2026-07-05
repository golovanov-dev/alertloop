# AlertLoop developer Makefile.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
ADMIN_DIR := web/admin

.PHONY: build run test vet fmt tidy release docker clean \
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

run: build ## Run all-in-one mode against a local SQLite database
	./bin/alertloop all

test: ## Run the full test suite
	go test ./...

vet: ## Static analysis
	go vet ./...

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
