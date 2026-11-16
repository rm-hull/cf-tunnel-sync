.PHONY: all build test lint ci tools css css-check clean

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

# Generate CSS from Tailwind source (uses npx with pinned version)
css:
	npx @tailwindcss/cli -i internal/web/assets/tailwind.css -o internal/web/static/app.css --minify

# Check CSS is up to date
css-check:
	@TMP=$$(mktemp); \
	npx @tailwindcss/cli -i internal/web/assets/tailwind.css -o "$$TMP" --minify 2>/dev/null; \
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
