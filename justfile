# Freshy — task runner
# Run `just` (or `just -l`) to list available recipes.

set shell := ["bash", "-c"]

# Default: list recipes.
default:
    @just --list

# Build the binary to ./bin/freshy.
build:
    mkdir -p bin
    go build -ldflags "-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" -o bin/freshy ./cmd/freshy

# Run go vet + build.
check:
    go vet ./...
    go build ./...

# Format source files.
fmt:
    gofmt -w -s .

# Tidy module dependencies.
tidy:
    go mod tidy

# Run the binary directly with go run (no build).
run CMD="--help":
    go run ./cmd/freshy {{CMD}}

# Smoke-test the local binary.
smoke: build
    ./bin/freshy version
    ./bin/freshy doctor || true

# Install: build + run the install script (symlinks units, enables timer).
install: build
    ./scripts/install.sh

# Uninstall everything (keeps config + data).
uninstall:
    ./scripts/uninstall.sh

# Uninstall and wipe config + data.
uninstall-purge:
    ./scripts/uninstall.sh --purge

# Clean build artifacts.
clean:
    rm -rf bin
    go clean ./...
