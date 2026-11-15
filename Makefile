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

# Run CI checks (format, vet, lint, test)
ci: fmt-check vet lint test

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

# Generate CSS (placeholder for S10)
css:
	@echo "CSS generation not yet implemented (S10)"

# Check CSS (placeholder for S10)
css-check:
	@echo "CSS check not yet implemented (S10)"

# Clean build artifacts
clean:
	rm -rf bin/
