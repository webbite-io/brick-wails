# Webbite Brick (Wails) Makefile
# Thin `make` wrapper around the wails3/Task build system, mirroring the
# target names used in ../webbite-brick-cli/Makefile for a familiar
# workflow across both repos.
#
# Unlike brick-cli (a plain Go CLI that cross-compiles trivially), this is a
# native GUI app: it uses CGO and per-OS webview/tray libraries, so builds
# only work for the OS you're running on. There is no build-all, and `release`
# only produces a Linux artifact — macOS/Windows packaging needs those native
# toolchains (or CI), see README.md's "Packaging" section.

APP_NAME := brick-ui
BIN_DIR := bin
DIST_DIR := dist

# Release artifacts are named after the Go arch (amd64/arm64) to match
# brick-cli's tarballs, while the AppImage that wails3 emits is named after the
# ELF arch (x86_64/aarch64). Keep both so the rename step doesn't guess.
GOARCH := $(shell go env GOARCH 2>/dev/null || echo amd64)
APPIMAGE_ARCH := $(if $(filter arm64,$(GOARCH)),aarch64,x86_64)

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

# Extract version from git tag (strip 'v' prefix), fallback to "dev":
#   on an exact tag   v1.2.3      -> 1.2.3
#   ahead of a tag    v1.2.3-4-g… -> 1.2.3-abc1234
#   no tags at all                -> dev
# The `|| echo dev` fallback has to key off the captured value rather than the
# exit status of the pipeline: `git describe ... | sed` exits 0 even when git
# failed and produced nothing, which otherwise yields an empty VERSION and
# artifact names like brick-ui--linux-amd64.tar.gz.
VERSION := $(shell \
	v=$$(git describe --tags --exact-match 2>/dev/null) || \
	v=$$(git describe --tags 2>/dev/null | sed 's/-[0-9]\+-g/-/'); \
	v=$$(echo "$$v" | sed 's/^v//'); \
	echo "$${v:-dev}")

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

# Webfonts for `make fonts`. Bunny serves woff2 only to browser-like agents.
FONTS_URL := https://fonts.bunny.net/css?family=inter:400,500,600|roboto-condensed:700&display=swap
FONTS_UA := Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36

.PHONY: all setup doctor dev build build-dev build-prod check-release-env run release release-to-github clean install version help test test-go test-frontend test-integration test-all fonts

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
	wails3 task build
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
	wails3 task run

# Build the Linux release artifact: the AppImage and its icon, wrapped in a
# tarball named like brick-cli's (brick-ui-<version>-linux-<arch>.tar.gz).
# The installer is deliberately NOT bundled — build/linux/appimage/install.sh
# is fetched from the repo and downloads this tarball, so it can be fixed
# without cutting a new release.
#
# This deliberately shells out to `wails3 generate appimage` rather than
# `wails3 task linux:create:appimage`: that task declares a `build` dependency
# and would rebuild the binary in a fresh Task invocation without the .env.prod
# values this Makefile exports, silently replacing the production build below
# with one that falls back to the localhost dev URLs at runtime.
#
# Needs network on first run — the AppImage generator downloads linuxdeploy and
# AppRun from GitHub, then caches them in build/linux/appimage/build.
release: build-prod
	@echo ""
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Creating AppImage...$(COLOR_RESET)"
	@rm -rf $(DIST_DIR)/stage
	@mkdir -p $(DIST_DIR)/stage/$(APP_NAME)
	@wails3 task linux:generate:dotdesktop
	@# linuxdeploy matches the icon by basename against the desktop file's
	@# Icon= key, so it has to be named $(APP_NAME).png, not appicon.png.
	@cp build/appicon.png $(DIST_DIR)/stage/$(APP_NAME).png
	@wails3 generate appimage \
		-binary $(BIN_DIR)/$(APP_NAME) \
		-icon $(DIST_DIR)/stage/$(APP_NAME).png \
		-desktopfile build/linux/$(APP_NAME).desktop \
		-outputdir $(DIST_DIR)/stage \
		-builddir build/linux/appimage/build
	@echo ""
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Assembling $(APP_NAME)-$(VERSION)-linux-$(GOARCH).tar.gz...$(COLOR_RESET)"
	@mv $(DIST_DIR)/stage/$(APP_NAME)-$(APPIMAGE_ARCH).AppImage $(DIST_DIR)/stage/$(APP_NAME)/$(APP_NAME).AppImage
	@chmod +x $(DIST_DIR)/stage/$(APP_NAME)/$(APP_NAME).AppImage
	@cp build/appicon.png $(DIST_DIR)/stage/$(APP_NAME)/$(APP_NAME).png
	@tar -czf $(DIST_DIR)/$(APP_NAME)-$(VERSION)-linux-$(GOARCH).tar.gz \
		-C $(DIST_DIR)/stage $(APP_NAME)
	@rm -rf $(DIST_DIR)/stage
	@echo ""
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Generating checksums...$(COLOR_RESET)"
	@cd $(DIST_DIR) && \
	if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum *.tar.gz > SHA256SUMS; \
	else \
		shasum -a 256 *.tar.gz > SHA256SUMS; \
	fi
	@echo "$(COLOR_GREEN)✓ Release artifact created$(COLOR_RESET)"
	@echo ""
	@ls -lh $(DIST_DIR)/*.tar.gz
	@echo ""
	@echo "Checksums (SHA256SUMS):"
	@cat $(DIST_DIR)/SHA256SUMS
	@echo ""
	@echo "Publish it with: make release-to-github"

# Publish the artifacts already sitting in dist/ (built by `make release`) as a
# GitHub release. Does not rebuild anything — the version published is whatever
# dist/SHA256SUMS says was actually built. Prompts before replacing a release
# that already exists. Mirrors brick-cli's target of the same name.
release-to-github:
	@if [ ! -f "$(DIST_DIR)/SHA256SUMS" ]; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) $(DIST_DIR)/SHA256SUMS not found. Run 'make release' first."; \
		exit 1; \
	fi; \
	if ! command -v gh >/dev/null 2>&1; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) gh CLI is required (https://cli.github.com/) — install it and run 'gh auth login' first."; \
		exit 1; \
	fi; \
	echo "$(COLOR_BOLD)$(COLOR_BLUE)Verifying release artifacts have production URLs baked in...$(COLOR_RESET)"; \
	if [ -z "$(ACC_API_URL)" ] || [ -z "$(STORAGE_API_URL)" ]; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) ACC_API_URL/STORAGE_API_URL are not available in this shell, so the artifacts can't be verified against them."; \
		echo "Make sure .env.prod is present (see .env.example) and re-run."; \
		exit 1; \
	fi; \
	bad=""; \
	tmp=$$(mktemp -d); \
	for f in $(DIST_DIR)/*.tar.gz; do \
		[ -f "$$f" ] || continue; \
		rm -rf "$$tmp"/*; \
		tar -xzf "$$f" -C "$$tmp" 2>/dev/null || { bad="$$bad $$f"; continue; }; \
		app="$$tmp/$(APP_NAME)/$(APP_NAME).AppImage"; \
		[ -f "$$app" ] || { bad="$$bad $$f"; continue; }; \
		: ; \
		( cd "$$tmp" && "$$app" --appimage-extract "usr/bin/$(APP_NAME)" >/dev/null 2>&1 ) || { bad="$$bad $$f"; continue; }; \
		bin="$$tmp/squashfs-root/usr/bin/$(APP_NAME)"; \
		[ -f "$$bin" ] || { bad="$$bad $$f"; continue; }; \
		grep -aqF "$(ACC_API_URL)" "$$bin" && grep -aqF "$(STORAGE_API_URL)" "$$bin" || bad="$$bad $$f"; \
	done; \
	rm -rf "$$tmp"; \
	if [ -n "$$bad" ]; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) refusing to publish — these artifacts don't have the expected production URLs ($(ACC_API_URL), $(STORAGE_API_URL)) baked in:$$bad"; \
		echo "Re-run 'make release' with production values set (see .env.prod) before publishing."; \
		exit 1; \
	fi; \
	echo "$(COLOR_GREEN)✓ Artifacts look production-ready$(COLOR_RESET)"; \
	repo=$$(gh repo view --json nameWithOwner -q .nameWithOwner) || exit 1; \
	rel_version=$$(awk '{print $$2}' $(DIST_DIR)/SHA256SUMS | sed -E 's/^$(APP_NAME)-(.+)-(linux)-(amd64|arm64)\.tar\.gz$$/\1/' | sort -u); \
	if [ -z "$$rel_version" ] || [ $$(echo "$$rel_version" | wc -l) -ne 1 ]; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) could not determine a single version from $(DIST_DIR)/SHA256SUMS; re-run 'make release' to rebuild a clean dist/."; \
		exit 1; \
	fi; \
	if [ "$$rel_version" = "dev" ]; then \
		echo "$(COLOR_YELLOW)Error:$(COLOR_RESET) refusing to publish version 'dev' — tag the commit first (git tag v0.1.0) and re-run 'make release'."; \
		exit 1; \
	fi; \
	assets="$(DIST_DIR)/SHA256SUMS"; \
	for f in $(DIST_DIR)/*.tar.gz; do \
		[ -f "$$f" ] && assets="$$assets $$f"; \
	done; \
	if gh release view "$$rel_version" --repo "$$repo" >/dev/null 2>&1; then \
		echo "$(COLOR_YELLOW)Release $$rel_version already exists on $$repo.$(COLOR_RESET)"; \
		printf "Replace it with the artifacts currently in $(DIST_DIR)/? (y/N): "; \
		read -r resp; \
		resp=$$(echo "$$resp" | tr '[:upper:]' '[:lower:]'); \
		if [ "$$resp" != "y" ] && [ "$$resp" != "yes" ]; then \
			echo "Aborted — existing release left untouched."; \
			exit 1; \
		fi; \
		echo "$(COLOR_BOLD)$(COLOR_BLUE)Replacing release $$rel_version on $$repo...$(COLOR_RESET)"; \
		gh release delete "$$rel_version" --repo "$$repo" --yes || exit 1; \
		gh release create "$$rel_version" $$assets --repo "$$repo" --title "$$rel_version" --generate-notes || exit 1; \
	else \
		echo "$(COLOR_BOLD)$(COLOR_BLUE)Creating release $$rel_version on $$repo...$(COLOR_RESET)"; \
		gh release create "$$rel_version" $$assets --repo "$$repo" --title "$$rel_version" --generate-notes || exit 1; \
	fi; \
	echo "$(COLOR_GREEN)✓ Released $$rel_version to $$repo$(COLOR_RESET)"; \
	echo ""; \
	echo "Users can now install with:"; \
	echo "  curl -fsSL https://raw.githubusercontent.com/$$repo/main/build/linux/appimage/install.sh | bash"

# Clean build artifacts (mirrors brick-cli's clean; keeps node_modules).
clean:
	@echo "$(COLOR_BOLD)$(COLOR_BLUE)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(BIN_DIR)
	@rm -rf $(DIST_DIR)
	@rm -rf frontend/dist
	@rm -rf .task
	@rm -rf build/linux/appimage/build
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
	@echo "  build-prod - Build for current platform, stripped (production; needs .env.prod)"
	@echo "  test       - Go unit tests (-race) + frontend typecheck and unit tests"
	@echo "  test-integration - End-to-end sync/onboarding tests (+ brick-cli compat if available)"
	@echo "  test-all   - test + test-integration"
	@echo "  run        - Run the last build"
	@echo "  release    - Build the Linux release tarball (AppImage) into dist/"
	@echo "  release-to-github - Publish dist/ artifacts as a GitHub release (prompts before replacing)"
	@echo "  clean      - Remove build artifacts (bin/, dist/, frontend/dist, .task)"
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
	@echo "  make release                                   # Linux tarball in dist/"
	@echo "  make release-to-github                         # Publish dist/ to GitHub"
	@echo ""
	@echo "$(COLOR_BOLD)Note:$(COLOR_RESET) unlike brick-cli, there is no build-all target, and"
	@echo "'release' produces a Linux artifact only. This is a native GUI app (CGO +"
	@echo "per-OS webview/tray libs), so builds only work for the OS you're running"
	@echo "on; macOS/Windows need CI or a native machine of that OS (see README.md's"
	@echo "Packaging section)."
	@echo ""
	@echo "$(COLOR_BOLD)Current version:$(COLOR_RESET) $(VERSION)"
