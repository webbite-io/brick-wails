package main

import (
	"embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/onboarding"
	"github.com/webbite-io/brick-wails/internal/runner"
	"github.com/webbite-io/brick-wails/internal/syncengine"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/tray/logo-wails-light.png
var trayIconLight []byte

//go:embed build/tray/logo-wails-dark.png
var trayIconDark []byte

// Build metadata and compile-time defaults, set via ldflags in production
// builds (see the Taskfiles / Makefile). Empty in dev builds, which instead
// load .env.local / .env.dev — the same rule brick-cli follows.
var (
	Version                 = "dev"
	DefaultAPIURL           = ""
	DefaultStorageAPIURL    = ""
	DefaultOAuthClientID    = ""
	DefaultOAuthScopes      = ""
	DefaultOAuthCallbackURL = ""
	DefaultWebURL           = ""
	DefaultHelpURL          = ""
)

func defaults() brickcfg.Defaults {
	return brickcfg.Defaults{
		APIURL: DefaultAPIURL, StorageAPIURL: DefaultStorageAPIURL, OAuthClientID: DefaultOAuthClientID,
		OAuthScopes: DefaultOAuthScopes, OAuthCallbackURL: DefaultOAuthCallbackURL, WebURL: DefaultWebURL, HelpURL: DefaultHelpURL,
	}
}

// openLog writes to <configDir>/brick-ui.log (truncated past 5 MB), plus
// stderr when DEBUG=true. Separate from brick-cli's daemon.log.
func openLog(dir string, debug bool) *log.Logger {
	var out io.Writer = io.Discard
	if err := os.MkdirAll(dir, 0o755); err == nil {
		p := filepath.Join(dir, "brick-ui.log")
		if info, err := os.Stat(p); err == nil && info.Size() > 5<<20 {
			_ = os.Truncate(p, 0)
		}
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			out = f
		}
	}
	if debug {
		out = io.MultiWriter(out, os.Stderr)
	}
	return log.New(out, "", log.LstdFlags)
}

// appEvents forwards runner output to the frontend and the log.
type appEvents struct {
	app      *application.App
	logger   *log.Logger
	onStatus func(runner.Status)
}

func (e *appEvents) Status(s runner.Status) {
	e.app.Event.Emit("brick:status", s)
	if e.onStatus != nil {
		e.onStatus(s)
	}
}
func (e *appEvents) Activity(ev syncengine.ActivityEvent) { e.app.Event.Emit("brick:activity", ev) }
func (e *appEvents) Logf(format string, args ...any)      { e.logger.Printf(format, args...) }

func main() {
	if brickcfg.ShouldLoadDevEnv(defaults()) {
		// Dev checkouts only (gitignored, absent from installs). Loaded in
		// order; a value from an earlier file wins.
		for _, f := range brickcfg.DevEnvFiles {
			_ = godotenv.Load(f)
		}
	}
	env := brickcfg.ResolveEnv(defaults())

	store, err := brickcfg.NewStore()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	logger := openLog(store.Dir(), env.Debug)
	logger.Printf("Webbite Brick %s starting (api=%s storage=%s config=%s)", Version, env.APIURL, env.StorageAPIURL, store.Path())
	if env.OAuthClientID == "" {
		logger.Printf("warning: OAUTH_CLIENT_ID is not set; login will fail (see .env.example)")
	}

	tokens, err := auth.NewTokenSource(store, env.APIURL, env.OAuthClientID)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	evs := &appEvents{logger: logger}
	run := runner.New(runner.Config{Env: env, Store: store, Tokens: tokens, Version: Version, Events: evs})
	flow := onboarding.New(env, store, tokens)

	var setupWindow *application.WebviewWindow
	syncSvc := &SyncService{runner: run}
	onbSvc := &OnboardingService{flow: flow, runner: run, window: func() *application.WebviewWindow { return setupWindow }}

	app := application.New(application.Options{
		Name:        "Webbite Brick",
		Description: "Webbite Brick — storage for all your devices",
		Services: []application.Service{
			application.NewService(syncSvc),
			application.NewService(onbSvc),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			// Tray-only app: no Dock icon, no menu bar.
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		OnShutdown: func() {
			logger.Printf("shutting down")
			flow.CancelLogin()
			run.Stop()
		},
	})
	evs.app, syncSvc.app, onbSvc.app = app, app, app

	// The popover is attached to the tray icon (Dropbox-style): hidden until
	// the icon is clicked, no taskbar presence; closing it just hides it.
	popover := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "Brick",
		Width:            360,
		Height:           460,
		AlwaysOnTop:      true,
		Hidden:           true,
		DisableResize:    true,
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/",
	})
	popover.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		popover.Hide()
		e.Cancel()
	})

	// The setup window hosts startup routing and the onboarding wizard (see
	// frontend/src/startup.ts). It starts hidden: on launch the frontend
	// routes, and only shows the window when the user has something to do —
	// a configured machine goes straight to syncing. Closing it hides it; the
	// tray's "Set Up Brick…" item brings it back.
	setupWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "Setup",
		Title:            "Webbite Brick",
		Width:            380,
		Height:           560,
		Hidden:           true,
		DisableResize:    true,
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/startup.html",
	})
	setupWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		flow.CancelLogin()
		setupWindow.Hide()
		e.Cancel()
	})
	openSetup := func() {
		flow.Reset()
		setupWindow.Show()
		setupWindow.Focus()
		app.Event.Emit("setup:open")
	}
	syncSvc.openSetup = openSetup

	tray := app.SystemTray.New()
	tray.SetTooltip("Webbite Brick")
	// SetDarkModeIcon only auto-switches on Windows, so react to theme
	// changes explicitly on every platform. IsDarkMode() is only reliable
	// once the app has started.
	applyTrayIcon := func() {
		if app.Env.IsDarkMode() {
			tray.SetIcon(trayIconDark)
		} else {
			tray.SetIcon(trayIconLight)
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { applyTrayIcon() })
	app.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) { applyTrayIcon() })

	menu := app.NewMenu()
	menu.Add("Open Brick Status").OnClick(func(*application.Context) { tray.ShowWindow() })
	openFolderItem := menu.Add("Open Brick Folder")
	openFolderItem.OnClick(func(*application.Context) { _ = syncSvc.OpenFolder() })
	menu.Add("Open Brick App").OnClick(func(*application.Context) { _ = app.Browser.OpenURL(env.WebURL) })
	menu.AddSeparator()
	setupItem := menu.Add("Set Up Brick…")
	setupItem.OnClick(func(*application.Context) { openSetup() })
	pauseItem := menu.Add("Pause Sync")
	pauseItem.OnClick(func(*application.Context) {
		if run.Status().State == "paused" {
			syncSvc.Resume()
		} else {
			syncSvc.Pause()
		}
	})
	menu.AddSeparator()
	menu.Add("Quit Brick").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	tray.AttachWindow(popover).WindowOffset(4)
	if runtime.GOOS == "linux" {
		// GNOME's AppIndicator support always reveals the menu on click;
		// Wails' default toggle of the attached window races it (under X11
		// both open). Route the click to the menu, GNOME's own convention.
		tray.OnClick(tray.OpenMenu)
	}

	// Keep the tray in step with status. Also opens the setup window when a
	// running sync loses its session, so the user is asked to log in again.
	var trayMu sync.Mutex
	lastState := ""
	updateTray := func(s runner.Status) {
		trayMu.Lock()
		defer trayMu.Unlock()
		prev := lastState
		lastState = s.State
		openFolderItem.SetEnabled(s.Folder != "")
		switch s.State {
		case runner.StateNotConfigured:
			tray.SetTooltip("Brick — not set up")
			setupItem.SetLabel("Set Up Brick…").SetHidden(false)
		case runner.StateAuthRequired:
			tray.SetTooltip("Brick — login required")
			setupItem.SetLabel("Log In Again…").SetHidden(false)
		case runner.StateLocked:
			tray.SetTooltip("Brick — the Brick CLI is syncing")
			setupItem.SetLabel("Start Syncing").SetHidden(false)
		case runner.StateStopped:
			tray.SetTooltip("Brick — not syncing")
			setupItem.SetLabel("Start Syncing").SetHidden(false)
		case "error":
			tray.SetTooltip("Brick — error: " + s.LastError)
			setupItem.SetHidden(true)
		default:
			tray.SetTooltip("Brick — " + s.State)
			setupItem.SetHidden(true)
		}
		pauseItem.SetEnabled(s.Running)
		if s.State == "paused" {
			pauseItem.SetLabel("Resume Sync")
		} else {
			pauseItem.SetLabel("Pause Sync")
		}
		if s.State == runner.StateAuthRequired && prev != "" && prev != runner.StateAuthRequired {
			logger.Printf("session expired; asking the user to log in again")
			openSetup()
		}
	}
	evs.onStatus = updateTray

	// Periodic status push too, so relative times in the popover stay fresh
	// even when nothing changes.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				evs.Status(run.Status())
			case <-app.Context().Done():
				return
			}
		}
	}()

	// Wails only handles SIGINT/SIGTERM in server mode. A session logout or
	// shutdown sends SIGTERM, so route it through app.Quit → OnShutdown, which
	// stops sync cleanly (state saved, device deregistered, lock released).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Printf("received termination signal")
		app.Quit()
	}()

	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
