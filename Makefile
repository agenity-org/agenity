.PHONY: build run version test fmt lint clean install

# Build metadata — embedded via -ldflags.
VERSION   := $(shell git describe --tags --dirty --always 2>/dev/null || echo "0.0.1-dev")
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDDATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X 'github.com/chepherd/chepherd/cmd.Version=$(VERSION)' \
	-X 'github.com/chepherd/chepherd/cmd.Commit=$(COMMIT)' \
	-X 'github.com/chepherd/chepherd/cmd.BuildDate=$(BUILDDATE)'

build: ## Build the chepherd binary into ./chepherd
	go build -ldflags "$(LDFLAGS)" -o chepherd .

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
