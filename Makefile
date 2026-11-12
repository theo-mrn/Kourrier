VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/theo-mrn/kourrier
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_KEY := kr_dev_local_insecure_key

# Tool versions, also pinned in .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

# Local ports (see docker-compose.yml), chosen not to clash with other stacks.
export KOURRIER_PORT ?= 8180
export KOURRIER_ADMIN_PORT ?= 9190
export REDIS_PORT ?= 6390
export KOURRIER_CONSOLE_PORT ?= 8181
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
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

.PHONY: vuln
vuln: ## Report known vulnerabilities reachable from the code
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

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
	KOURRIER_CONSOLE_ENABLED=true \
	KOURRIER_CONSOLE_ADDR=127.0.0.1:$(KOURRIER_CONSOLE_PORT) \
	KOURRIER_CONSOLE_TOKEN_FILE=dev/console-token \
	KOURRIER_CONSOLE_SECURE_COOKIE=false \
	go run ./cmd/kourrier

.PHONY: up
up: ## Start Redis, NATS and Kourrier in Docker; e-mails go to the console (make logs)
	docker compose up --build -d --wait
	@echo "API: http://localhost:$(KOURRIER_PORT)  Metrics: http://localhost:$(KOURRIER_ADMIN_PORT)/metrics"
	@echo "Console: http://localhost:$(KOURRIER_CONSOLE_PORT) (token in dev/console-token)"
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

.PHONY: event
event: ## Publish the demo CloudEvent to NATS (routed by examples/routes.yaml)
	docker compose run --rm nats-init 'nats --server nats://nats:4222 pub users.signed_up \
	  -H "Content-Type:application/cloudevents+json" "$$(cat /events/user-signed-up.json)"'

.PHONY: event-http
event-http: ## Push the demo CloudEvent to the HTTP sink (POST /v1/events)
	@curl -sS -X POST http://localhost:$(KOURRIER_PORT)/v1/events \
	  -H "Authorization: Bearer $(DEV_KEY)" \
	  -H "Content-Type: application/cloudevents+json" \
	  --data-binary @examples/events/user-signed-up.json
	@echo

.PHONY: routes-check
routes-check: ## Validate the example routes file
	go run ./cmd/kourrier routes check -tenants dev/tenants.yaml examples/routes.yaml

CONSOLE := pnpm --dir web/console

.PHONY: console-install
console-install: ## Install the web console dependencies (Node 24, pnpm)
	$(CONSOLE) install --frozen-lockfile

.PHONY: console-build
console-build: console-install ## Build the web console into the binary (then make build)
	$(CONSOLE) build

.PHONY: console-dev
console-dev: console-install ## Serve the console with hot reload against the local stack (make up)
	$(CONSOLE) dev

.PHONY: console-check
console-check: console-install ## Check formatting, lint, type-check and test the web console
	$(CONSOLE) format:check
	$(CONSOLE) lint
	$(CONSOLE) typecheck
	$(CONSOLE) test

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: helm-lint
helm-lint: ## Lint and render the Helm chart
	helm lint deploy/helm/kourrier
	helm template kourrier deploy/helm/kourrier >/dev/null
	helm template kourrier deploy/helm/kourrier -f deploy/helm/kourrier/ci/events-values.yaml >/dev/null
	helm template kourrier deploy/helm/kourrier -f deploy/helm/kourrier/ci/console-values.yaml >/dev/null
