package main

import (
	"embed"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/joho/godotenv"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/tray/logo-wails-light.png
var trayIconLight []byte

//go:embed build/tray/logo-wails-dark.png
var trayIconDark []byte

// defaultWebURL is used when STORAGE_WEB_URL isn't set in the environment.
// .env.dev (which sets it in local checkouts) is gitignored and deliberately
// absent from production installs, so packaged builds fall back to this.
const defaultWebURL = "https://brick.webbite.io"

func main() {
	// .env.dev is only present in local dev checkouts (gitignored); production
	// installs won't have it, so a missing-file error here is expected and safe
	// to ignore — same pattern brick-cli uses for its own .env loading.
	_ = godotenv.Load(".env.dev")

	startupSvc := &StartupService{}

	app := application.New(application.Options{
		Name:        "Webbite Brick",
		Description: "Tray companion for the Webbite Brick CLI",
		Services: []application.Service{
			application.NewService(&BrickService{}),
			application.NewService(startupSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			// Tray-only app: no Dock icon, no menu bar.
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
	})
	startupSvc.app = app

	// The popover window is attached to the tray icon (Dropbox-style): it
	// starts hidden, has no taskbar presence, and toggles open/closed
	// on tray click rather than behaving like an ordinary window.
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:          "Brick",
		Width:         360,
		Height:        460,
		AlwaysOnTop:   true,
		Hidden:        true,
		DisableResize: true,
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/",
	})

	// Clicking outside the popover should close it rather than quit the app
	// — closing this window is just hiding the tray popover, not exiting.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})

	// The startup window checks whether brick is already running (talking
	// to it through BrickService, same as the popover) and, if not, walks
	// through self-test/setup/install to get it running — see startup.go
	// and frontend/src/startup.ts. It's a real, closable window (unlike the
	// popover above): once the frontend's startup flow gets brick running,
	// it closes itself.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "Startup",
		Title:            "Brick Setup",
		Width:            440,
		Height:           480,
		AlwaysOnTop:      true,
		DisableResize:    true,
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/startup.html",
	})

	tray := app.SystemTray.New()
	tray.SetTooltip("Webbite Brick")

	// SetDarkModeIcon only auto-switches on Windows; macOS and Linux treat it
	// as a no-op alias for SetIcon. Rather than rely on that per-platform
	// split, react to theme changes explicitly so all three platforms behave
	// the same way. IsDarkMode() isn't reliable until the app has finished
	// starting (ApplicationStarted), so the initial icon is applied there
	// rather than here.
	applyTrayIcon := func() {
		if app.Env.IsDarkMode() {
			tray.SetIcon(trayIconDark)
		} else {
			tray.SetIcon(trayIconLight)
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		applyTrayIcon()
	})
	app.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) {
		applyTrayIcon()
	})

	brick := &BrickService{}

	menu := app.NewMenu()
	openItem := menu.Add("Open Brick Status")
	openItem.OnClick(func(ctx *application.Context) {
		tray.ShowWindow()
	})
	openFolderItem := menu.Add("Open Brick Folder")
	if folder := storageSyncFolder(); folder != "" {
		openFolderItem.OnClick(func(ctx *application.Context) {
			_ = app.Browser.OpenFile(folder)
		})
	} else {
		openFolderItem.SetEnabled(false)
	}
	openWebappItem := menu.Add("Open Brick App")
	webURL := os.Getenv("STORAGE_WEB_URL")
	if webURL == "" {
		webURL = defaultWebURL
	}
	openWebappItem.OnClick(func(ctx *application.Context) {
		_ = app.Browser.OpenURL(webURL)
	})
	menu.AddSeparator()
	pauseItem := menu.Add("Pause Sync")
	pauseItem.OnClick(func(ctx *application.Context) {
		status, _ := brick.Status()
		if status.State == "paused" {
			_ = brick.Resume()
		} else {
			_ = brick.Pause()
		}
	})
	menu.AddSeparator()
	menu.Add("Quit Brick (stop syncing)").OnClick(func(ctx *application.Context) {
		_ = brick.QuitBrick()
	})
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	tray.SetMenu(menu)
	tray.AttachWindow(window).WindowOffset(4)
	if runtime.GOOS == "linux" {
		// GNOME's AppIndicator/StatusNotifierItem support always reveals the
		// menu on a tray click rather than emitting a distinct "activate"
		// (that's only sent on double-click) — see
		// https://github.com/ubuntu/gnome-shell-extension-appindicator. Wails'
		// default click handler instead toggles the attached window, which
		// races GNOME's own menu popup; under X11 specifically this shows
		// both the window and the menu from a single click. Routing the
		// click straight to the menu matches GNOME's own convention (and
		// what already happens under Wayland) and avoids the double-open.
		// "Open Brick Status" in the menu still reaches the window.
		tray.OnClick(tray.OpenMenu)
	}

	// Poll brick's status every 2s (matches the phased rollout in brick-cli's
	// control API plan: push via a /v1/events WebSocket is a later addition,
	// polling is enough for a first tray icon). Also emit it as a Wails event
	// so the frontend can subscribe without polling itself.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			status, err := brick.Status()
			if err == nil {
				app.Event.Emit("brick:status", status)
				switch status.State {
				case "not-running":
					tray.SetTooltip("Brick — not running")
					pauseItem.SetLabel("Pause Sync").SetEnabled(false)
				case "paused":
					tray.SetTooltip("Brick — paused")
					pauseItem.SetLabel("Resume Sync").SetEnabled(true)
				case "error":
					tray.SetTooltip("Brick — error: " + status.LastError)
					pauseItem.SetLabel("Pause Sync").SetEnabled(true)
				default:
					tray.SetTooltip("Brick — " + status.State)
					pauseItem.SetLabel("Pause Sync").SetEnabled(true)
				}
			}
			select {
			case <-ticker.C:
			case <-app.Context().Done():
				return
			}
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
