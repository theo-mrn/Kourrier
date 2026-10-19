VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/theo-mrn/kourrier
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_KEY := kr_dev_local_insecure_key

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/kourrier ./cmd/kourrier

.PHONY: test
test: ## Run the tests with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run the tests and open the coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: lint
lint: ## Run go vet and golangci-lint
	go vet ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run

.PHONY: run
run: ## Run locally (Redis on localhost, templates from examples/, console mailer)
	KOURRIER_LOG_FORMAT=text \
	KOURRIER_TENANTS_FILE=dev/tenants.yaml \
	KOURRIER_STORAGE_PROVIDER=filesystem \
	KOURRIER_STORAGE_DIR=examples/templates \
	KOURRIER_MAILER_PROVIDER=console \
	go run ./cmd/kourrier

.PHONY: up
up: ## Start the full development stack (Redis, RustFS, Mailpit, Kourrier)
	docker compose up --build -d
	@echo "API: http://localhost:8080  Mailpit: http://localhost:8025  Metrics: http://localhost:9090/metrics"

.PHONY: down
down: ## Stop the development stack
	docker compose down

.PHONY: send
send: ## Send the demo welcome e-mail through the local API
	@curl -sS -X POST http://localhost:8080/v1/emails \
	  -H "Authorization: Bearer $(DEV_KEY)" \
	  -H "Content-Type: application/json" \
	  -H "Idempotency-Key: $$(uuidgen)" \
	  -d '{"to":[{"email":"jane@example.com","name":"Jane"}],"template":{"name":"welcome","locale":"fr","data":{"name":"Jane","login_url":"https://example.com/login"}}}'
	@echo

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: helm-lint
helm-lint: ## Lint and render the Helm chart
	helm lint deploy/helm/kourrier
	helm template kourrier deploy/helm/kourrier >/dev/null
