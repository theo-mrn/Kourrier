VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/theo-mrn/kourrier
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_KEY := kr_dev_local_insecure_key

# Local ports (see docker-compose.yml), chosen not to clash with other stacks.
export KOURRIER_PORT ?= 8180
export KOURRIER_ADMIN_PORT ?= 9190
export REDIS_PORT ?= 6390
COMPOSE_FULL := docker compose -f docker-compose.yml -f docker-compose.full.yml

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
run: ## Run from source with its own Redis; e-mails are printed in this terminal
	docker compose up -d --wait redis
	KOURRIER_HTTP_ADDR=127.0.0.1:$(KOURRIER_PORT) \
	KOURRIER_ADMIN_ADDR=127.0.0.1:$(KOURRIER_ADMIN_PORT) \
	KOURRIER_REDIS_ADDR=127.0.0.1:$(REDIS_PORT) \
	KOURRIER_LOG_FORMAT=text \
	KOURRIER_TENANTS_FILE=dev/tenants.yaml \
	KOURRIER_STORAGE_PROVIDER=filesystem \
	KOURRIER_STORAGE_DIR=examples/templates \
	KOURRIER_TEMPLATES_CACHE_TTL=0s \
	KOURRIER_RATE_LIMIT_PER_SECOND=0 \
	KOURRIER_MAILER_PROVIDER=console \
	go run ./cmd/kourrier

.PHONY: up
up: ## Start Redis and Kourrier in Docker; e-mails go to the console (make logs)
	docker compose up --build -d --wait
	@echo "API: http://localhost:$(KOURRIER_PORT)  Metrics: http://localhost:$(KOURRIER_ADMIN_PORT)/metrics"
	@echo "Send the demo e-mail with 'make send', read it with 'make logs'."

.PHONY: up-full
up-full: ## Same with the production-like drivers: S3 templates (RustFS), SMTP (Mailpit)
	$(COMPOSE_FULL) up --build -d --wait
	@echo "API: http://localhost:$(KOURRIER_PORT)  Mailpit: http://localhost:$${MAILPIT_PORT:-8125}"

.PHONY: logs
logs: ## Follow Kourrier's output (the console driver prints the e-mails there)
	docker compose logs -f kourrier

.PHONY: down
down: ## Stop the development stack (both modes)
	$(COMPOSE_FULL) down

.PHONY: send
send: ## Send the demo welcome e-mail through the local API
	@curl -sS -X POST http://localhost:$(KOURRIER_PORT)/v1/emails \
	  -H "Authorization: Bearer $(DEV_KEY)" \
	  -H "Content-Type: application/json" \
	  -H "Idempotency-Key: $$(uuidgen)" \
	  -d '{"to":[{"email":"jane@example.com","name":"Jane"}],"template":{"name":"welcome","locale":"fr","data":{"name":"Jane","login_url":"https://example.com/login"}}}'
	@echo

TEMPLATE ?= welcome
LOCALE   ?= fr
DATA     ?= {"name":"Jane","login_url":"https://example.com/login"}

.PHONY: preview
preview: ## Render a template without sending it and open it (TEMPLATE=, LOCALE=, DATA=)
	@mkdir -p tmp
	@curl -sS --fail-with-body -X POST "http://localhost:$(KOURRIER_PORT)/v1/templates/$(TEMPLATE)/preview" \
	  -H "Authorization: Bearer $(DEV_KEY)" \
	  -H "Content-Type: application/json" \
	  -H "Accept: text/html" \
	  -d '{"locale":"$(LOCALE)","data":$(DATA)}' -o tmp/preview.html
	@echo "tmp/preview.html"
	@open tmp/preview.html 2>/dev/null || xdg-open tmp/preview.html 2>/dev/null || true

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: helm-lint
helm-lint: ## Lint and render the Helm chart
	helm lint deploy/helm/kourrier
	helm template kourrier deploy/helm/kourrier >/dev/null
