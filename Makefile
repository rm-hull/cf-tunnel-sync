.PHONY: all build test lint ci tools css css-check clean docker-build goreleaser-snapshot

# Default target
all: build

# Build the binary
build:
	go build -o bin/cf-tunnel-sync .

# Run tests
test:
	go test -race ./...

# Run linter
lint:
	golangci-lint run

# Run CI checks (format, vet, lint, test, css)
ci: fmt-check vet lint test css-check

# Format code
fmt:
	go fmt ./...
	which gofumpt >/dev/null 2>&1 && gofumpt -w . || true

# Check formatting
fmt-check:
	go fmt ./...
	@if command -v gofumpt >/dev/null 2>&1; then \
		gofumpt -d .; \
	fi

# Run go vet
vet:
	go vet ./...

# Install tools
tools:
	# Install golangci-lint
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest; \
	fi
	# Install gofumpt
	go install mvdan.cc/gofumpt@latest
	# Install Node.js dependencies (Tailwind CSS)
	# Requires Node.js: brew install node (macOS) or sudo apt install nodejs npm (Ubuntu)
	npm install

# Generate CSS from Tailwind source (uses npx with pinned version)
css:
	@VERSION=$$(cat tools/tailwind.version); \
	npx @tailwindcss/cli@$$VERSION -i internal/web/assets/tailwind.css -o internal/web/static/app.css --minify

# Install npm dependencies (Tailwind CSS)
npm-install:
	npm install

# Check CSS is up to date
css-check: npm-install
	@VERSION=$$(cat tools/tailwind.version); \
	TMP=$$(mktemp); \
	npx @tailwindcss/cli@$$VERSION -i internal/web/assets/tailwind.css -o "$$TMP" --minify 2>/dev/null; \
	if ! diff -q internal/web/static/app.css "$$TMP" >/dev/null 2>&1; then \
		echo "ERROR: CSS is out of date. Run 'make css' to rebuild."; \
		rm -f "$$TMP"; \
		exit 1; \
	fi; \
	rm -f "$$TMP"; \
	echo "CSS is up to date"

# Clean build artifacts
clean:
	rm -rf bin/

# Build Docker image (local dev, multi-stage)
docker-build:
	docker build -t cf-tunnel-sync:test .

# GoReleaser snapshot build (local dry-run)
goreleaser-snapshot:
	goreleaser release --snapshot --clean
