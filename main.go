package main

import (
	"embed"
	"log"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := application.New(application.Options{
		Name:        "RequestBite Brick",
		Description: "Tray companion for RequestBite Brick storage sync",
		Services: []application.Service{
			application.NewService(&BrickService{}),
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

	tray := app.SystemTray.New()
	tray.SetTooltip("RequestBite Brick")
	if runtime.GOOS == "darwin" {
		// Template icons adapt to the menu bar's light/dark mode automatically.
		tray.SetTemplateIcon(icons.SystrayMacTemplate)
	} else {
		tray.SetIcon(icons.SystrayLight)
		tray.SetDarkModeIcon(icons.SystrayDark)
	}
	// TODO: swap the placeholder Wails icons above for a Brick-branded tray
	// icon (idle/syncing/error/paused variants) once one is designed.

	brick := &BrickService{}

	menu := app.NewMenu()
	openItem := menu.Add("Open Brick")
	openItem.OnClick(func(ctx *application.Context) {
		tray.ShowWindow()
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
