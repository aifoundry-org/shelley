# Shelley Makefile

.PHONY: build build-release build-custom build-linux-aarch64 build-linux-x86 test test-go test-e2e ui ui-release serve clean help templates demo deb docker

# Default target
all: build

# Build templates into tarballs
templates:
	@for dir in templates/*/; do \
		name=$$(basename "$$dir"); \
		tar -czf "templates/$$name.tar.gz" -C "templates/$$name" --exclude='.DS_Store' .; \
	done

# Build the UI and Go binary
build: ui templates
	@echo "Building Shelley..."
	go build -o bin/shelley ./cmd/shelley

# Release/deploy build. Identical to `build`, but the embedded UI build info
# omits srcDir so the binary does NOT run the dev-only "UI build is stale!"
# staleness check at startup. Use this for any binary that will be DEPLOYED
# (e.g. hot-swapped into the running shelley.service) on the same machine it
# was built on, where a later git checkout/edit would otherwise brick it.
build-release: ui-release templates
	@echo "Building Shelley (release)..."
	go build -o bin/shelley ./cmd/shelley

# Build a customized Shelley binary (see the customizing-shelley skill).
# Stamps the release tag this branch diverged from (latest release tag at the
# merge-base with origin/main) and marks the build as customized, so the
# version dialog knows the build has diverged from mainline and offers
# rebase-style upgrades instead of binary self-updates.
build-custom: ui templates
	@set -e; \
	CANON="$$HOME/.config/shelley/shelley-customization"; \
	if [ "$$(pwd -P)" != "$$(cd "$$CANON" 2>/dev/null && pwd -P)" ]; then \
		echo "warning: building outside $$CANON; the version dialog's rebase-upgrade flow assumes that checkout" >&2; \
	fi; \
	git fetch --tags origin main; \
	BASE=$$(git merge-base HEAD origin/main); \
	TAG=$$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$$BASE") || { \
		echo "error: no release tag reachable from the merge-base with origin/main; is this a shallow clone? (need a full clone with tags)" >&2; \
		exit 1; \
	}; \
	SHA=$$(git rev-parse --short HEAD); \
	go build -ldflags "\
	  -X shelley.exe.dev/version.Version=$${TAG#v}-custom.$$SHA \
	  -X shelley.exe.dev/version.Tag=$$TAG \
	  -X shelley.exe.dev/version.Customized=true" \
	  -o bin/shelley ./cmd/shelley; \
	echo "Built bin/shelley: customized, based on $$TAG, HEAD $$SHA"

# Build for Linux (auto-detect architecture)
build-linux: ui templates
	@echo "Building Shelley for Linux..."
	@ARCH=$$(uname -m); \
	case $$ARCH in \
		x86_64) GOARCH=amd64 ;; \
		aarch64|arm64) GOARCH=arm64 ;; \
		*) echo "Unsupported architecture: $$ARCH" && exit 1 ;; \
	esac; \
	GOOS=linux GOARCH=$$GOARCH go build -o bin/shelley-linux ./cmd/shelley

# Build for Linux ARM64
build-linux-aarch64: ui templates
	@echo "Building Shelley for Linux ARM64..."
	GOOS=linux GOARCH=arm64 go build -o bin/shelley-linux-aarch64 ./cmd/shelley

# Build for Linux x86_64
build-linux-x86: ui templates
	@echo "Building Shelley for Linux x86_64..."
	GOOS=linux GOARCH=amd64 go build -o bin/shelley-linux-x86 ./cmd/shelley

# Build UI
ui:
	@cd ui && pnpm install --frozen-lockfile --silent && pnpm run --silent build

# Build UI for release/deploy (see build-release): embeds an empty srcDir so
# the staleness self-check is disabled in the resulting binary.
ui-release:
	@cd ui && SHELLEY_RELEASE_BUILD=1 pnpm install --frozen-lockfile --silent && SHELLEY_RELEASE_BUILD=1 pnpm run --silent build

# Run Go tests
test-go: ui
	@echo "Running Go tests..."
	go test -v ./...

# Run end-to-end tests
test-e2e: ui
	@echo "Running E2E tests..."
	cd ui && pnpm run test:e2e

# Run E2E tests in headed mode (with visible browser)
test-e2e-headed: ui
	@echo "Running E2E tests (headed)..."
	cd ui && pnpm run test:e2e:headed

# Run E2E tests in UI mode
test-e2e-ui: ui
	@echo "Opening E2E test UI..."
	cd ui && pnpm run test:e2e:ui

# Run all tests
test: test-go test-e2e

# Serve Shelley with predictable model for testing
serve-test: ui
	@echo "Starting Shelley with predictable model..."
	go run ./cmd/shelley --predictable-only --db test.db serve

# Serve Shelley normally
serve: ui
	@echo "Starting Shelley..."
	go run ./cmd/shelley serve

# --- Container packaging ---------------------------------------------------
#
# Two-step flow: `deb` builds the Debian package with goreleaser, and `docker`
# feeds that package into the container build. `docker` depends on `deb`, so
# `make docker` does the whole thing.

# Docker image tag (override with `make docker IMAGE=myrepo/shelley:tag`).
IMAGE ?= shelley:latest
# Host architecture -> goreleaser's deb arch suffix.
# NB: avoid a shell `case` here -- the `)` in its patterns prematurely closes
# make's $(shell ...) call and breaks parsing. A sed pipeline has no such issue.
DEB_ARCH := $(shell uname -m | sed -e 's/^x86_64$$/amd64/' -e 's/^aarch64$$/arm64/')

# Build the shelley .deb via goreleaser (snapshot: no git tag required).
# Output lands in dist/shelley_<version>_linux_<arch>.deb.
deb: ui-release templates
	@echo "Building Debian package with goreleaser..."
	goreleaser release --snapshot --clean --skip=publish,announce,validate

# Build the container image, feeding it the freshly built .deb. Uses --squash
# to flatten the image so intermediate apt/dpkg layers aren't shipped.
docker: deb
	@set -e; \
	deb=$$(ls -t dist/shelley_*_linux_$(DEB_ARCH).deb | head -n1); \
	if [ -z "$$deb" ]; then echo "no .deb found in dist/ for arch $(DEB_ARCH)" >&2; exit 1; fi; \
	echo "Building $(IMAGE) from $$deb..."; \
	cp "$$deb" shelley.deb; \
	trap 'rm -f shelley.deb' EXIT; \
	docker build --squash \
		--build-arg SHELLEY_DEB=shelley.deb \
		-t $(IMAGE) .

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -rf ui/dist/
	rm -rf ui/node_modules/
	rm -rf ui/test-results/
	rm -rf ui/playwright-report/
	rm -f *.db
	rm -f templates/*.tar.gz
	rm -rf dist/
	rm -f shelley.deb

# Build and (re)start the demo server
demo:
	@./demo.py

# Show help
help:
	@echo "Shelley Build Commands:"
	@echo ""
	@echo "  build         Build UI, templates, and Go binary"
	@echo "  build-release Build for deploy/hot-swap (no dev staleness check embedded)"
	@echo "  build-custom  Build a customized binary stamped as diverged from mainline"
	@echo "  build-linux-aarch64  Build for Linux ARM64"
	@echo "  build-linux-x86      Build for Linux x86_64"
	@echo "  ui            Build UI only"
	@echo "  templates     Build template tarballs"
	@echo "  test          Run all tests (Go + E2E)"
	@echo "  test-go       Run Go tests only"
	@echo "  test-e2e      Run E2E tests (headless)"
	@echo "  test-e2e-headed  Run E2E tests (visible browser)"
	@echo "  test-e2e-ui   Open E2E test UI"
	@echo "  serve         Start Shelley server"
	@echo "  serve-test    Start Shelley with predictable model"
	@echo "  clean         Clean build artifacts"
	@echo "  deb           Build the Debian package via goreleaser"
	@echo "  docker        Build the flattened Docker image (builds deb first)"
	@echo "  demo          Build and (re)start the demo server"
	@echo "  help          Show this help"

