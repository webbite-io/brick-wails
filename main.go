package main

import (
	"embed"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/logfile"
	"github.com/webbite-io/brick-wails/internal/onboarding"
	"github.com/webbite-io/brick-wails/internal/runner"
	"github.com/webbite-io/brick-wails/internal/syncengine"
	"github.com/webbite-io/brick-wails/internal/trayicon"
	"github.com/webbite-io/brick-wails/internal/update"
	"github.com/webbite-io/brick-wails/internal/webapp"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/tray/logo-wails-light.png
var trayIconLight []byte

//go:embed build/tray/logo-wails-dark.png
var trayIconDark []byte

// menuDotSize is the pixel size of the blue dot drawn beside the tray menu's
// "Install Update" item — menu item icons are rendered at roughly the text
// height, so it's sized for that rather than for the tray icon.
const menuDotSize = 16

// trayIconSet is one theme's tray icon in each of its three faces: plain, a
// blue dot for a waiting update, a yellow one for paused sync.
type trayIconSet struct{ plain, update, paused []byte }

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
	DefaultWebOAuthClientID = ""
	DefaultWebURL           = ""
	DefaultHelpURL          = ""
)

func defaults() brickcfg.Defaults {
	return brickcfg.Defaults{
		APIURL: DefaultAPIURL, StorageAPIURL: DefaultStorageAPIURL, OAuthClientID: DefaultOAuthClientID,
		OAuthScopes: DefaultOAuthScopes, OAuthCallbackURL: DefaultOAuthCallbackURL,
		WebOAuthClientID: DefaultWebOAuthClientID, WebURL: DefaultWebURL, HelpURL: DefaultHelpURL,
	}
}

// openLog writes to <configDir>/brick.log — brick-cli's log file, kept to the
// same 10,000-line cap (see internal/logfile) — plus stderr when DEBUG=true.
// Lines are tagged "UI: " after the timestamp (Lmsgprefix), so a shared log
// says which app wrote what; brick-cli's own lines carry no tag.
// Logging is best-effort: a log file that can't be opened (a read-only home,
// say) leaves the app running with stderr-only output.
func openLog(dir string, debug bool) *log.Logger {
	var out io.Writer = io.Discard
	if w, err := logfile.Open(dir); err == nil {
		out = w
	} else if debug {
		fmt.Fprintf(os.Stderr, "could not open %s: %v\n", logfile.Name, err)
	}
	if debug {
		out = io.MultiWriter(out, os.Stderr)
	}
	return log.New(out, "UI: ", log.LstdFlags|log.Lmsgprefix)
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
		brickcfg.LoadDevEnv(brickcfg.DevEnvFiles...)
	}
	env := brickcfg.ResolveEnv(defaults())

	store, err := brickcfg.NewStore()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// `brick-wails --dry-run` reports what a sync pass would do and exits,
	// without a window, a tray icon or the instance lock — the headless way
	// to exercise the reconcile logic. Handled before anything starts up so
	// it leaves no trace in the log either.
	maybeRunDryRun(env, store)

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

	var setupWindow, updateWindow *application.WebviewWindow
	syncSvc := &SyncService{runner: run}
	onbSvc := &OnboardingService{flow: flow, runner: run, window: func() *application.WebviewWindow { return setupWindow }}
	updSvc := &UpdateService{
		logger:  logger,
		version: Version,
		window:  func() *application.WebviewWindow { return updateWindow },
	}

	app := application.New(application.Options{
		Name:        "Webbite Brick",
		Description: "Webbite Brick — storage for all your devices",
		Services: []application.Service{
			application.NewService(syncSvc),
			application.NewService(onbSvc),
			application.NewService(updSvc),
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
	evs.app, syncSvc.app, onbSvc.app, updSvc.app = app, app, app, app

	// The popover is attached to the tray icon (Dropbox-style): hidden until
	// the icon is clicked, no taskbar presence; closing it just hides it.
	popover := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "Brick",
		Width:  420,
		Height: 650,
		// Same sizing as the setup window: resizable upwards only, with the
		// default size as the floor.
		MinWidth:         420,
		MinHeight:        650,
		AlwaysOnTop:      true,
		Hidden:           true,
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
	// tray's "Set Up Brick" item brings it back.
	setupWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "Setup",
		Title:  "Webbite Brick",
		Width:  420,
		Height: 650,
		// Resizable upwards only: the default size is also the floor, so a step
		// with a long list can be given more room but the layout never gets
		// squeezed below what it was designed for.
		MinWidth:         420,
		MinHeight:        650,
		Hidden:           true,
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/startup.html",
	})
	setupWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		flow.CancelLogin()
		setupWindow.Hide()
		e.Cancel()
	})

	// The update window is a small, fixed-size prompt: it starts hidden, and
	// UpdateService shows it when the launch check finds a newer release or
	// when the user picks the tray's update item themselves.
	updateWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "Update",
		Title:            "Webbite Brick",
		Width:            380,
		Height:           210,
		MinWidth:         380,
		MinHeight:        210,
		MaxWidth:         380,
		MaxHeight:        210,
		Hidden:           true,
		AlwaysOnTop:      true,
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/update.html",
	})
	updateWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		updateWindow.Hide()
		e.Cancel()
	})
	// The frontend shows the window itself once it has routed and laid out the
	// screen (OnboardingService.ShowWindow). Showing it here instead would
	// present the previous screen's frame for an instant before the new one
	// renders.
	openSetup := func() {
		flow.Reset()
		app.Event.Emit("setup:open")
	}
	syncSvc.openSetup = openSetup

	tray := app.SystemTray.New()
	tray.SetTooltip("Webbite Brick")
	// Both themes' badged faces are composited once here, at startup. A failed
	// composite costs the dot, not the tray icon.
	badge := func(icon []byte, c color.NRGBA) []byte {
		badged, err := trayicon.Badge(icon, c)
		if err != nil {
			logger.Printf("tray badge: %v", err)
			return icon
		}
		return badged
	}
	faces := func(icon []byte) trayIconSet {
		return trayIconSet{
			plain:  icon,
			update: badge(icon, trayicon.UpdateBlue),
			paused: badge(icon, trayicon.PausedYellow),
		}
	}
	lightIcons, darkIcons := faces(trayIconLight), faces(trayIconDark)

	// SetDarkModeIcon only auto-switches on Windows, so react to theme
	// changes explicitly on every platform. IsDarkMode() is only reliable
	// once the app has started.
	//
	// iconMu makes reading the two states and setting the icon one step: the
	// update check and the status feed both land here from their own
	// goroutines, and without it the loser of a race could leave the icon
	// showing what the winner had already moved on from.
	var iconMu sync.Mutex
	var updatePending, syncPaused atomic.Bool
	applyTrayIcon := func() {
		iconMu.Lock()
		defer iconMu.Unlock()
		icons := lightIcons
		if app.Env.IsDarkMode() {
			icons = darkIcons
		}
		switch {
		// A waiting update outranks paused sync: it's the one the user has
		// something to do about.
		case updatePending.Load():
			tray.SetIcon(icons.update)
		case syncPaused.Load():
			tray.SetIcon(icons.paused)
		default:
			tray.SetIcon(icons.plain)
		}
	}
	// Seeded before the app runs, so the tray registers with the Brick icon:
	// an unset icon registers Wails' own logo, which would show until the
	// ApplicationStarted call below replaced it. IsDarkMode() is false this
	// early, so this lands on the light face and that call corrects it.
	applyTrayIcon()
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { applyTrayIcon() })
	app.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) { applyTrayIcon() })

	menu := app.NewMenu()
	menu.Add("Open Brick Status").OnClick(func(*application.Context) { tray.ShowWindow() })
	openFolderItem := menu.Add("Open Brick Folder")
	openFolderItem.OnClick(func(*application.Context) { _ = syncSvc.OpenFolder() })
	// Off the UI thread: handing the session to the browser takes a round
	// trip to the auth server first (see webAppOpener), and the menu must not
	// sit open while that happens.
	webApp := &webapp.Opener{Env: env, Store: store, Tokens: tokens, Logger: logger, OpenURL: app.Browser.OpenURL}
	menu.Add("Open Brick App").OnClick(func(*application.Context) { go webApp.Open(auth.WebTargetFiles) })
	menu.AddSeparator()
	// Hidden until the startup routing has concluded what the machine needs
	// (see updateTray): the runner starts out not-configured, so an item shown
	// from the start would offer setup on every launch, including the ones
	// that are a second away from syncing.
	setupItem := menu.Add("Set Up Brick")
	setupItem.SetHidden(true)
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
	// Offers whatever a check has already turned up, and otherwise runs one on
	// the spot — see updSvc.onPending below for the "Install Update" face of
	// this item.
	updateItem := menu.Add("Check for Updates")
	updateItem.OnClick(func(*application.Context) { go updSvc.offerOrCheck() })
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

	// One place where a found update changes the tray: the icon gains its blue
	// dot and the menu item becomes the offer to install it, carrying the same
	// dot. Both stay until the app is actually replaced, so dismissing the
	// window doesn't hide that an update is still waiting.
	updateDot, err := trayicon.Dot(menuDotSize, trayicon.UpdateBlue)
	if err != nil {
		logger.Printf("menu dot: %v", err)
	}
	updSvc.onPending = func(*update.Info) {
		updatePending.Store(true)
		updateItem.SetLabel("Install Update")
		if updateDot != nil {
			updateItem.SetBitmap(updateDot)
		}
		applyTrayIcon()
	}

	// The launch check runs immediately, then every CheckInterval — a tray app
	// can stay running for weeks. A dev build (Version == "dev") never checks,
	// matching brick-cli's own isRunningInDevelopment gate.
	if Version != "dev" {
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			go func() {
				updSvc.checkOnStartup()
				t := time.NewTicker(update.CheckInterval)
				defer t.Stop()
				for {
					select {
					case <-t.C:
						updSvc.checkInBackground()
					case <-app.Context().Done():
						return
					}
				}
			}()
		})
	}

	// Keep the tray in step with status. Also opens the setup window when a
	// running sync loses its session, so the user is asked to log in again.
	var trayMu sync.Mutex
	lastState := ""
	// Set once the frontend's startup routing has reached a verdict — see
	// onbSvc.onSettled below.
	var startupSettled atomic.Bool
	updateTray := func(s runner.Status) {
		trayMu.Lock()
		defer trayMu.Unlock()
		prev := lastState
		lastState = s.State
		openFolderItem.SetEnabled(s.Folder != "")
		needsSetup := true
		switch s.State {
		case runner.StateNotConfigured:
			tray.SetTooltip("Brick — not set up")
			setupItem.SetLabel("Set Up Brick")
		case runner.StateAuthRequired:
			tray.SetTooltip("Brick — login required")
			setupItem.SetLabel("Log In Again…")
		case runner.StateLocked:
			tray.SetTooltip("Brick — the Brick CLI is syncing")
			setupItem.SetLabel("Start Syncing")
		case runner.StateStopped:
			tray.SetTooltip("Brick — not syncing")
			setupItem.SetLabel("Start Syncing")
		case "error":
			tray.SetTooltip("Brick — error: " + s.LastError)
			needsSetup = false
		default:
			tray.SetTooltip("Brick — " + s.State)
			needsSetup = false
		}
		// The item stays away until startup routing has settled: until then
		// the state is only the runner's starting point, not a conclusion
		// about the machine.
		setupItem.SetHidden(!needsSetup || !startupSettled.Load())
		pauseItem.SetEnabled(s.Running)
		if s.State == "paused" {
			pauseItem.SetLabel("Resume Sync")
		} else {
			pauseItem.SetLabel("Pause Sync")
		}
		// The icon carries the paused state too. Only on a change: status
		// lands here every couple of seconds, and each SetIcon costs a tray
		// refresh.
		if paused := s.State == "paused"; syncPaused.Swap(paused) != paused {
			applyTrayIcon()
		}
		if s.State == runner.StateAuthRequired && prev != "" && prev != runner.StateAuthRequired {
			logger.Printf("session expired; asking the user to log in again")
			openSetup()
		}
	}
	evs.onStatus = updateTray
	// The startup routing in frontend/src/startup.ts decides what this machine
	// needs — and only then is the runner's state a verdict the tray can offer
	// the user something about.
	onbSvc.onSettled = func() {
		if !startupSettled.Swap(true) {
			updateTray(run.Status())
		}
	}

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
