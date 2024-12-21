##@ General
.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


##@ Dev
start-services: ## Start the dependant Docker services (database).
	@echo "Starting services..."
	@docker compose up -d

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: ## Run tests.
	go test -v ./...

.PHONY: check
check: fmt vet test ## Check the code

.PHONY: run
run: ## Run the server with hot reload.
	@command -v entr >/dev/null 2>&1 || { echo >&2 "entry required but not installed: https://github.com/eradman/entr"; exit 1; }
	@[ -f .env ] && source .env
	while true; do find . | entr -rcd go run .; sleep 0.5; done


