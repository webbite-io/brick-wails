# Webbite Brick (Wails) Makefile
# Thin `make` wrapper around the wails3/Task build system, mirroring the
# target names used in ../webbite-brick-cli/Makefile for a familiar
# workflow across both repos.
#
# Unlike brick-cli (a plain Go CLI that cross-compiles trivially), this is a
# native GUI app: it uses CGO and per-OS webview/tray libraries, so builds
# only work for the OS you're running on. There is no build-all/release
# target — packaging for macOS/Windows needs those native toolchains (or CI),
# see README.md's "Packaging" section.

APP_NAME := brick-ui
BIN_DIR := bin

# Pin the wails3 CLI to the exact version this module depends on (go.mod),
# rather than `go install .../wails3@latest`, so the CLI never drifts ahead
# of what the app is actually built against.
WAILS_VERSION := $(shell awk '/github.com\/wailsapp\/wails\/v3 /{print $$3; exit}' go.mod)

# Extract version from git tag (strip 'v' prefix), fallback to "dev"
VERSION := $(shell if git describe --tags --exact-match 2>/dev/null >/dev/null; then \
	git describe --tags --exact-match | sed 's/^v//'; \
else \
	git describe --tags 2>/dev/null | sed 's/^v//' | sed 's/-[0-9]\+-g/-/' || echo "dev"; \
fi)

# Colors for output
COLOR_RESET := \033[0m
COLOR_BOLD := \033[1m
COLOR_GREEN := \033[32m
COLOR_BLUE := \033[34m
COLOR_YELLOW := \033[33m

# System packages needed to build/run a Wails v3 GUI app with a system tray
# on Debian/Ubuntu. GTK4/WebKitGTK 6.0 is this project's default target (see
# build/linux/nfpm/nfpm.yaml); GTK3/webkit2gtk-4.1 is only needed if built
# with `-tags gtk3` (legacy, unused here). Printed by `make doctor`, never
# installed automatically.
LINUX_APT_DEPS := build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libayatana-appindicator3-dev

.PHONY: all setup doctor dev build build-dev build-prod run package clean install version help

# Default target
all: build-prod

# Check that the tools/libraries needed to build are present (no installs).
doctor:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Checking build prerequisites...$(COLOR_RESET)"
	@ok=1; \
	command -v go >/dev/null 2>&1 && echo "  [ok] go: $$(go version)" || { echo "  [missing] go — https://go.dev/dl/"; ok=0; }; \
	command -v node >/dev/null 2>&1 && echo "  [ok] node: $$(node --version)" || { echo "  [missing] node — https://nodejs.org/en/download/"; ok=0; }; \
	command -v npm >/dev/null 2>&1 && echo "  [ok] npm: $$(npm --version)" || { echo "  [missing] npm (bundled with Node.js)"; ok=0; }; \
	command -v wails3 >/dev/null 2>&1 && echo "  [ok] wails3: $$(wails3 version 2>/dev/null || echo installed)" || { echo "  [missing] wails3 — run 'make setup' (installs via go install)"; ok=0; }; \
	command -v task >/dev/null 2>&1 && echo "  [ok] task: $$(task --version)" || echo "  [missing] task — https://taskfile.dev/installation/ (wails3 shells out to it)"; \
	if command -v pkg-config >/dev/null 2>&1; then \
		for lib in gtk4 webkitgtk-6.0 ayatana-appindicator3-0.1; do \
			pkg-config --exists $$lib 2>/dev/null && echo "  [ok] $$lib" || { echo "  [missing] $$lib"; ok=0; }; \
		done; \
	else \
		echo "  [missing] pkg-config"; ok=0; \
	fi; \
	if [ $$ok -ne 1 ]; then \
		echo ""; \
		echo "$(COLOR_YELLOW)On Debian/Ubuntu, install missing system libraries with:$(COLOR_RESET)"; \
		echo "  sudo apt install $(LINUX_APT_DEPS)"; \
		echo "Then install Node.js (https://nodejs.org/en/download/) if missing."; \
	else \
		echo ""; \
		echo "$(COLOR_GREEN)✓ All prerequisites found$(COLOR_RESET)"; \
	fi

# Install the wails3 CLI (if missing) and frontend dependencies.
# Requires pkg-config + the GTK/WebKit dev headers up front: `go install`ing
# wails3 itself shells out to pkg-config during its build, so without these
# it fails with a confusing "pkg-config: executable file not found" instead
# of a clear message.
setup:
	@if ! command -v pkg-config >/dev/null 2>&1 || ! pkg-config --exists gtk4 webkitgtk-6.0 ayatana-appindicator3-0.1 2>/dev/null; then \
		echo "$(COLOR_YELLOW)Missing system libraries required to build wails3.$(COLOR_RESET)"; \
		echo "On Debian/Ubuntu, install them first with:"; \
		echo "  sudo apt install $(LINUX_APT_DEPS)"; \
		echo "Then re-run 'make setup'."; \
		exit 1; \
	fi
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Installing wails3 CLI v$(WAILS_VERSION) (pinned to go.mod)...$(COLOR_RESET)"
	@go install github.com/wailsapp/wails/v3/cmd/wails3@$(WAILS_VERSION)
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Installing frontend dependencies...$(COLOR_RESET)"
	@cd frontend && npm install
	@echo "$(COLOR_GREEN)✓ Setup complete$(COLOR_RESET) (run 'make doctor' to check system libraries)"

# Run with hot reload (frontend + backend), like brick-cli's `make dev`.
dev:
	wails3 dev

# Development build: unstripped, faster iteration, matches brick-cli's build-dev.
build-dev:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Building $(APP_NAME) v$(VERSION) (dev)...$(COLOR_RESET)"
	wails3 task build DEV=true
	@echo "$(COLOR_GREEN)✓ Build complete: $(BIN_DIR)/$(APP_NAME)$(COLOR_RESET)"

# Production build: stripped, trimmed, -tags production.
build-prod:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Building $(APP_NAME) v$(VERSION) (production)...$(COLOR_RESET)"
	wails3 task build
	@echo "$(COLOR_GREEN)✓ Build complete: $(BIN_DIR)/$(APP_NAME)$(COLOR_RESET)"

build: build-prod

# Run the last build.
run:
	wails3 task run

# Build native packages for this OS (.deb/.rpm/AppImage on Linux).
package:
	wails3 task package

# Clean build artifacts (mirrors brick-cli's clean; keeps node_modules).
clean:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(BIN_DIR)
	@rm -rf frontend/dist
	@rm -rf .task
	@echo "$(COLOR_GREEN)✓ Clean complete$(COLOR_RESET)"

# Install locally for testing (to ~/.local/bin).
install: build-prod
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Installing $(APP_NAME) to ~/.local/bin...$(COLOR_RESET)"
	@mkdir -p ~/.local/bin
	@cp $(BIN_DIR)/$(APP_NAME) ~/.local/bin/
	@chmod +x ~/.local/bin/$(APP_NAME)
	@echo "$(COLOR_GREEN)✓ Installed to ~/.local/bin/$(APP_NAME)$(COLOR_RESET)"
	@echo ""
	@if echo "$$PATH" | grep -q "$$HOME/.local/bin"; then \
		echo "Ready to use: $(APP_NAME)"; \
	else \
		echo "$(COLOR_YELLOW)Warning:$(COLOR_RESET) ~/.local/bin is not in your PATH"; \
		echo "Add to PATH: export PATH=\"$$HOME/.local/bin:$$PATH\";"; \
	fi

# Show version
version:
	@echo "$(APP_NAME) v$(VERSION)"

# Show help
help:
	@echo "$(COLOR_BOLD)Webbite Brick (Wails) - Build System$(COLOR_RESET)"
	@echo ""
	@echo "$(COLOR_BOLD)Usage:$(COLOR_RESET)"
	@echo "  make [target]"
	@echo ""
	@echo "$(COLOR_BOLD)Targets:$(COLOR_RESET)"
	@echo "  doctor     - Check that build tools and system libraries are present"
	@echo "  setup      - Install wails3 CLI and frontend (npm) dependencies"
	@echo "  dev        - Run with hot reload (frontend + backend)"
	@echo "  build-dev  - Build for current platform, unstripped (dev)"
	@echo "  build-prod - Build for current platform, stripped (production)"
	@echo "  run        - Run the last build"
	@echo "  package    - Build native packages for this OS (.deb/.rpm/AppImage on Linux)"
	@echo "  clean      - Remove build artifacts (bin/, frontend/dist, .task)"
	@echo "  install    - Build using build-prod and install to ~/.local/bin (for testing)"
	@echo "  version    - Show version information"
	@echo "  help       - Show this help message"
	@echo ""
	@echo "$(COLOR_BOLD)Examples:$(COLOR_RESET)"
	@echo "  make doctor                                   # Check prerequisites first"
	@echo "  make setup                                    # One-time: install wails3 + npm deps"
	@echo "  make dev                                       # Run with hot reload"
	@echo "  make build-dev                                 # Quick dev build"
	@echo "  make install                                   # Build + install locally"
	@echo ""
	@echo "$(COLOR_BOLD)Note:$(COLOR_RESET) unlike brick-cli, there is no build-all/release target."
	@echo "This is a native GUI app (CGO + per-OS webview/tray libs), so builds"
	@echo "only work for the OS you're running on; other OS targets need CI or a"
	@echo "native machine of that OS (see README.md's Packaging section)."
	@echo ""
	@echo "$(COLOR_BOLD)Current version:$(COLOR_RESET) $(VERSION)"
