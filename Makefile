.PHONY: build run version test fmt lint clean install agent-image all-images

# Build metadata — embedded via -ldflags.
VERSION   := $(shell git describe --tags --dirty --always 2>/dev/null || echo "0.0.1-dev")
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDDATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# #273 — bake the scripts/agent-entrypoint.sh SHA256 into the chepherd
# binary at build time so cmd/run.go can detect a stale chepherd-agent
# image on boot (image's /usr/local/bin/agent-entrypoint SHA mismatch
# vs this baked SHA → loud warning + rebuild instruction). Walker hit
# the silent-stale case where the chepherd binary container was rebuilt
# post-#257 but the chepherd-agent image was NOT — every new spawn
# used the pre-#257 OLD entrypoint with the `[ ! -e ]` skip-if-exists
# guard that re-pinned stale credentials.
AGENT_ENTRYPOINT_SHA := $(shell sha256sum scripts/agent-entrypoint.sh 2>/dev/null | awk '{print $$1}')

LDFLAGS := -s -w \
	-X 'github.com/chepherd/chepherd/cmd.Version=$(VERSION)' \
	-X 'github.com/chepherd/chepherd/cmd.Commit=$(COMMIT)' \
	-X 'github.com/chepherd/chepherd/cmd.BuildDate=$(BUILDDATE)' \
	-X 'github.com/chepherd/chepherd/internal/runtime.expectedAgentEntrypointSHA=$(AGENT_ENTRYPOINT_SHA)'

build: ## Build the chepherd binary into ./chepherd
	go build -ldflags "$(LDFLAGS)" -o chepherd .

agent-image: ## Rebuild the chepherd-agent:latest container image (#273 — required after scripts/agent-entrypoint.sh changes)
	podman build -f Dockerfile.agent -t chepherd-agent:latest .

all-images: build agent-image ## Build both chepherd binary and chepherd-agent image (#273)

run: build ## Build + run with no args (opens TUI when wired up)
	./chepherd

version: build ## Print build info from the freshly-built binary
	./chepherd version

status: build ## Run the status snapshot against live state
	./chepherd status

test: ## Run unit tests
	go test ./... -race -count=1

fmt: ## Format all Go files
	gofmt -w -s .

lint: ## Run go vet + staticcheck if available
	go vet ./...
	@if command -v staticcheck > /dev/null; then staticcheck ./...; else echo "(staticcheck not installed)"; fi

install: ## Install to $GOPATH/bin
	go install -ldflags "$(LDFLAGS)" ./...

clean: ## Remove build artifacts
	rm -f chepherd
	rm -rf dist/
