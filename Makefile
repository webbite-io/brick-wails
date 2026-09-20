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

# Host OS. The app is a native GUI (CGO + per-OS webview/tray libraries), so
# prerequisites, the install location and the packages produced all differ
# per OS; the targets below branch on this.
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
HOST_OS := darwin
else ifeq ($(UNAME_S),Linux)
HOST_OS := linux
else
HOST_OS := $(UNAME_S)
endif

# Production builds bake ACC_API_URL, STORAGE_API_URL, OAUTH_* and
# STORAGE_*_URL in at compile time (see BRICK_LDFLAGS in Taskfile.yml and the
# Default* vars in main.go). Like brick-cli, load them from .env.prod when it
# exists (gitignored); CI sets them as real env vars instead. Dev builds
# (make dev / build-dev) don't bake anything and read .env.local at runtime.
#
# They are only exported for build-prod (target-specific exports below):
# exporting them globally would hand `make dev` *empty* env vars when there's
# no .env.prod, which would hide the values in .env.local.
-include .env.prod

# Pin the wails3 CLI to the exact version this module depends on (go.mod),
# rather than `go install .../wails3@latest`, so the CLI never drifts ahead
# of what the app is actually built against.
WAILS_VERSION := $(shell awk '/github.com\/wailsapp\/wails\/v3 /{print $$3; exit}' go.mod)

# `go install` drops wails3 in $(go env GOPATH)/bin, which isn't on PATH by
# default (notably in a fresh macOS shell). Prefer the one on PATH, and fall
# back to GOPATH/bin so `make dev`/`make build` work either way; `make setup`
# prints a hint when only the fallback is usable.
GOBIN_DIR := $(shell go env GOBIN 2>/dev/null)
ifeq ($(GOBIN_DIR),)
GOBIN_DIR := $(shell go env GOPATH 2>/dev/null)/bin
endif
WAILS3 := $(shell command -v wails3 2>/dev/null || echo $(GOBIN_DIR)/wails3)

# Node version required by the frontend toolchain (Vite: ^20.19 || >=22.12).
# Checked by `make doctor` — the macOS system Node is often far older.
NODE_VERSION_CHECK := const [a,b]=process.versions.node.split('.').map(Number); \
	process.exit(((a==20&&b>=19)||(a==22&&b>=12)||a>=23)?0:1)

# Extract version from git tag (strip 'v' prefix), fallback to "dev"
VERSION := $(shell if git describe --tags --exact-match 2>/dev/null >/dev/null; then \
	git describe --tags --exact-match | sed 's/^v//'; \
else \
	v=$$(git describe --tags 2>/dev/null | sed 's/^v//' | sed 's/-[0-9]\+-g/-/'); \
	echo "$${v:-dev}"; \
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

# macOS needs no third-party libraries: the webview (WKWebView) and the tray
# (NSStatusItem) are system frameworks. All it needs is the Xcode command
# line tools for clang/the SDK, since the build is CGO. Builds target
# macOS 12+ (see build/darwin/Taskfile.yml).

# Webfonts for `make fonts`. Bunny serves woff2 only to browser-like agents.
FONTS_URL := https://fonts.bunny.net/css?family=inter:400,500,600|roboto-condensed:700&display=swap
FONTS_UA := Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36

.PHONY: all setup doctor dev build build-dev build-prod check-release-env run package clean install version help test test-go test-frontend test-integration test-all fonts

# Default target
all: build-prod

# Check that the tools/libraries needed to build are present (no installs).
# The native-GUI prerequisites differ per OS: GTK4/WebKitGTK/AppIndicator dev
# packages on Linux, just the Xcode command line tools on macOS.
doctor:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Checking build prerequisites ($(HOST_OS))...$(COLOR_RESET)"
	@ok=1; \
	command -v go >/dev/null 2>&1 && echo "  [ok] go: $$(go version)" || { echo "  [missing] go — https://go.dev/dl/"; ok=0; }; \
	if command -v node >/dev/null 2>&1; then \
		if node -e "$(NODE_VERSION_CHECK)" 2>/dev/null; then \
			echo "  [ok] node: $$(node --version)"; \
		else \
			echo "  [too old] node: $$(node --version) — Vite needs ^20.19 or >=22.12"; ok=0; \
		fi; \
	else \
		echo "  [missing] node — https://nodejs.org/en/download/"; ok=0; \
	fi; \
	command -v npm >/dev/null 2>&1 && echo "  [ok] npm: $$(npm --version)" || { echo "  [missing] npm (bundled with Node.js)"; ok=0; }; \
	if [ -x "$(WAILS3)" ] || command -v wails3 >/dev/null 2>&1; then \
		echo "  [ok] wails3: $$($(WAILS3) version 2>&1 || echo installed) ($(WAILS3))"; \
		command -v wails3 >/dev/null 2>&1 || echo "       note: not on PATH — add $(GOBIN_DIR) to it (the targets here use the full path)"; \
	else \
		echo "  [missing] wails3 — run 'make setup' (installs via go install)"; ok=0; \
	fi; \
	command -v task >/dev/null 2>&1 && echo "  [ok] task: $$(task --version)" || echo "  [missing] task — https://taskfile.dev/installation/ (wails3 shells out to it)"; \
	if [ "$(HOST_OS)" = "darwin" ]; then \
		xcode-select -p >/dev/null 2>&1 && echo "  [ok] Xcode command line tools: $$(xcode-select -p)" || { echo "  [missing] Xcode command line tools"; ok=0; }; \
		command -v clang >/dev/null 2>&1 && echo "  [ok] clang: $$(clang --version | head -1)" || { echo "  [missing] clang"; ok=0; }; \
	elif [ "$(HOST_OS)" = "linux" ]; then \
		if command -v pkg-config >/dev/null 2>&1; then \
			for lib in gtk4 webkitgtk-6.0 ayatana-appindicator3-0.1; do \
				pkg-config --exists $$lib 2>/dev/null && echo "  [ok] $$lib" || { echo "  [missing] $$lib"; ok=0; }; \
			done; \
		else \
			echo "  [missing] pkg-config"; ok=0; \
		fi; \
	fi; \
	if [ $$ok -ne 1 ]; then \
		echo ""; \
		if [ "$(HOST_OS)" = "darwin" ]; then \
			echo "$(COLOR_YELLOW)On macOS, install the missing pieces with:$(COLOR_RESET)"; \
			echo "  xcode-select --install        # compiler + SDK (CGO build)"; \
			echo "  brew install node             # or download from https://nodejs.org/en/download/"; \
		else \
			echo "$(COLOR_YELLOW)On Debian/Ubuntu, install missing system libraries with:$(COLOR_RESET)"; \
			echo "  sudo apt install $(LINUX_APT_DEPS)"; \
			echo "Then install Node.js (https://nodejs.org/en/download/) if missing."; \
		fi; \
	else \
		echo ""; \
		echo "$(COLOR_GREEN)✓ All prerequisites found$(COLOR_RESET)"; \
	fi

# Install the wails3 CLI (if missing) and frontend dependencies.
# The native toolchain has to be in place first, so the failure is a clear
# message rather than a confusing one from deep inside a build:
# on Linux `go install`ing wails3 shells out to pkg-config (without the
# GTK/WebKit headers it dies with "pkg-config: executable file not found"),
# and on macOS the CGO build needs the Xcode command line tools.
setup:
ifeq ($(HOST_OS),linux)
	@if ! command -v pkg-config >/dev/null 2>&1 || ! pkg-config --exists gtk4 webkitgtk-6.0 ayatana-appindicator3-0.1 2>/dev/null; then \
		echo "$(COLOR_YELLOW)Missing system libraries required to build wails3.$(COLOR_RESET)"; \
		echo "On Debian/Ubuntu, install them first with:"; \
		echo "  sudo apt install $(LINUX_APT_DEPS)"; \
		echo "Then re-run 'make setup'."; \
		exit 1; \
	fi
endif
ifeq ($(HOST_OS),darwin)
	@if ! xcode-select -p >/dev/null 2>&1; then \
		echo "$(COLOR_YELLOW)The Xcode command line tools are required to build (CGO).$(COLOR_RESET)"; \
		echo "Install them first with:"; \
		echo "  xcode-select --install"; \
		echo "Then re-run 'make setup'."; \
		exit 1; \
	fi
	@if command -v node >/dev/null 2>&1 && ! node -e "$(NODE_VERSION_CHECK)" 2>/dev/null; then \
		echo "$(COLOR_YELLOW)Node $$(node --version) is too old for the frontend toolchain (Vite needs ^20.19 or >=22.12).$(COLOR_RESET)"; \
		echo "macOS often has an old Node from a system-wide installer ahead of a newer one on PATH."; \
		echo "Install a current Node (brew install node, or https://nodejs.org/en/download/) and re-run 'make setup'."; \
		exit 1; \
	fi
endif
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Installing wails3 CLI v$(WAILS_VERSION) (pinned to go.mod)...$(COLOR_RESET)"
	@go install github.com/wailsapp/wails/v3/cmd/wails3@$(WAILS_VERSION)
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Installing frontend dependencies...$(COLOR_RESET)"
	@cd frontend && npm install
	@echo "$(COLOR_GREEN)✓ Setup complete$(COLOR_RESET) (run 'make doctor' to check system libraries)"
	@command -v wails3 >/dev/null 2>&1 || { \
		echo ""; \
		echo "$(COLOR_YELLOW)Note:$(COLOR_RESET) wails3 was installed to $(GOBIN_DIR), which is not on your PATH."; \
		echo "The targets in this Makefile call it by full path, but to run it yourself add:"; \
		echo "  export PATH=\"$(GOBIN_DIR):$$PATH\"        # fish: fish_add_path -g $(GOBIN_DIR)"; \
	}

# Run with hot reload (frontend + backend), like brick-cli's `make dev`.
dev:
	$(WAILS3) dev

# Development build: unstripped, faster iteration, matches brick-cli's build-dev.
build-dev:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Building $(APP_NAME) v$(VERSION) (dev)...$(COLOR_RESET)"
	$(WAILS3) task build DEV=true
	@echo "$(COLOR_GREEN)✓ Build complete: $(BIN_DIR)/$(APP_NAME)$(COLOR_RESET)"

# Production build: stripped, trimmed, -tags production, with the values
# from .env.prod (or the environment) baked in.
build-prod: export ACC_API_URL := $(ACC_API_URL)
build-prod: export STORAGE_API_URL := $(STORAGE_API_URL)
build-prod: export OAUTH_CLIENT_ID := $(OAUTH_CLIENT_ID)
build-prod: export OAUTH_SCOPES := $(OAUTH_SCOPES)
build-prod: export OAUTH_CALLBACK_URL := $(OAUTH_CALLBACK_URL)
build-prod: export STORAGE_WEB_URL := $(STORAGE_WEB_URL)
build-prod: export STORAGE_HELP_URL := $(STORAGE_HELP_URL)
build-prod: check-release-env
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Building $(APP_NAME) v$(VERSION) (production)...$(COLOR_RESET)"
	$(WAILS3) task build
	@echo "$(COLOR_GREEN)✓ Build complete: $(BIN_DIR)/$(APP_NAME)$(COLOR_RESET)"

build: build-prod

# Fail fast rather than silently baking empty values into a production build,
# which would then fall back to the localhost dev URLs at runtime.
check-release-env:
	@missing=""; \
	[ -n "$(ACC_API_URL)" ] || missing="$$missing ACC_API_URL"; \
	[ -n "$(STORAGE_API_URL)" ] || missing="$$missing STORAGE_API_URL"; \
	[ -n "$(OAUTH_CLIENT_ID)" ] || missing="$$missing OAUTH_CLIENT_ID"; \
	if [ -n "$$missing" ]; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) missing required production env vars:$$missing"; \
		echo "Add them to .env.prod (see .env.example), or export them in your shell (as CI does)."; \
		exit 1; \
	fi

# --- Tests ---
# Go unit tests live under internal/ and never import Wails, so they need no
# GTK/WebKit headers. Integration tests (real fsnotify, fake auth + storage
# servers, optionally a real brick-cli build) are behind the "integration"
# build tag; set BRICK_CLI_DIR to a brick-cli checkout (default ../brick-cli)
# to include the cross-CLI compatibility tests.
test: test-go test-frontend

test-go:
	go test -race ./internal/...

test-frontend:
	cd frontend && npm run typecheck && npm test

test-integration:
	go test -race -tags integration -count=1 ./integration/...

test-all: test test-integration

# Re-download the self-hosted webfonts from fonts.bunny.net and regenerate
# frontend/public/fonts.css. Only needed when changing families or weights;
# the woff2 files are committed so normal builds never hit the network.
# Note: Bunny's /css2 endpoint silently honours only the first family=, so
# this uses the v1 pipe syntax to get both families in one stylesheet.
fonts:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Fetching webfonts from fonts.bunny.net...$(COLOR_RESET)"
	@rm -rf frontend/public/fonts && mkdir -p frontend/public/fonts
	@curl -sSf -A "$(FONTS_UA)" "$(FONTS_URL)" -o /tmp/brick-fonts.css
	@grep -oE 'https://fonts\.bunny\.net/[^)]+\.woff2' /tmp/brick-fonts.css \
		| sort -u > /tmp/brick-fonts-urls.txt
	@cd frontend/public/fonts && xargs -n1 -P8 curl -sSfO < /tmp/brick-fonts-urls.txt
	@{ \
		echo "/* Self-hosted Inter (400/500/600) and Roboto Condensed (700)."; \
		echo " * Generated from fonts.bunny.net; woff2 files live in /fonts."; \
		echo " * Regenerate with: make fonts"; \
		echo " * font-display:swap + unicode-range preserved, so each subset loads lazily."; \
		echo " */"; \
		echo; \
		sed -E \
			-e "s#, url\(https://fonts\.bunny\.net/[^)]+\.woff\) format\('woff'\)##" \
			-e 's#url\(https://fonts\.bunny\.net/[^/]+/files/([^)]+\.woff2)\)#url(/fonts/\1)#' \
			-e 's/[[:space:]]+$$//' \
			/tmp/brick-fonts.css; \
	} > frontend/public/fonts.css
	@echo "$(COLOR_GREEN)✓ $$(ls frontend/public/fonts | wc -l) woff2 files, $$(du -sh frontend/public/fonts | cut -f1)$(COLOR_RESET)"

# Run the last build.
run:
	$(WAILS3) task run

# Build native packages for this OS: .deb/.rpm/AppImage on Linux, a
# (ad-hoc signed) bin/$(APP_NAME).app bundle on macOS.
package:
	$(WAILS3) task package

# Clean build artifacts (mirrors brick-cli's clean; keeps node_modules).
clean:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(BIN_DIR)
	@rm -rf frontend/dist
	@rm -rf .task
	@echo "$(COLOR_GREEN)✓ Clean complete$(COLOR_RESET)"

# Install locally for testing: ~/.local/bin on Linux; on macOS a tray app
# needs to run from an .app bundle (that's what carries the icon, the bundle
# identifier and the accessory activation policy), so install that to
# ~/Applications instead of dropping a bare binary on PATH.
install: build-prod
ifeq ($(HOST_OS),darwin)
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Bundling $(APP_NAME).app...$(COLOR_RESET)"
	@$(WAILS3) task darwin:create:app:bundle
	@mkdir -p ~/Applications
	@rm -rf "$$HOME/Applications/$(APP_NAME).app"
	@cp -R "$(BIN_DIR)/$(APP_NAME).app" ~/Applications/
	@echo "$(COLOR_GREEN)✓ Installed to ~/Applications/$(APP_NAME).app$(COLOR_RESET)"
	@echo ""
	@echo "Start it with: open -a \"$$HOME/Applications/$(APP_NAME).app\""
	@echo "It runs as a menu bar app — no Dock icon, look for the tray icon."
else
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
endif

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
	@echo "  build-prod - Build for current platform, stripped (production; needs .env.prod)"
	@echo "  test       - Go unit tests (-race) + frontend typecheck and unit tests"
	@echo "  test-integration - End-to-end sync/onboarding tests (+ brick-cli compat if available)"
	@echo "  test-all   - test + test-integration"
	@echo "  run        - Run the last build"
	@echo "  package    - Build native packages for this OS (.deb/.rpm/AppImage on Linux, .app on macOS)"
	@echo "  clean      - Remove build artifacts (bin/, frontend/dist, .task)"
	@echo "  install    - Build using build-prod and install for testing"
	@echo "               (~/.local/bin on Linux, ~/Applications/$(APP_NAME).app on macOS)"
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
	@echo "$(COLOR_BOLD)Host OS:$(COLOR_RESET) $(HOST_OS)"
	@echo ""
	@echo "$(COLOR_BOLD)Note:$(COLOR_RESET) unlike brick-cli, there is no build-all/release target."
	@echo "This is a native GUI app (CGO + per-OS webview/tray libs), so builds"
	@echo "only work for the OS you're running on; other OS targets need CI or a"
	@echo "native machine of that OS (see README.md's Packaging section)."
	@echo ""
	@echo "$(COLOR_BOLD)Current version:$(COLOR_RESET) $(VERSION)"
