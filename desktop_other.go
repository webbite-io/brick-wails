//go:build !linux

package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ensureDesktopEntry is Linux-only: elsewhere an app's name and icon come
// from its bundle or executable, not a .desktop entry.
func ensureDesktopEntry(*log.Logger) string { return "" }

// startAtLogin is Linux-only for now: macOS would register a login item
// instead. A nil one leaves the tray item and the wizard's checkbox out.
type startAtLogin struct{}

func newStartAtLogin(string, *log.Logger) *startAtLogin { return nil }

func (*startAtLogin) Supported() bool                                 { return false }
func (*startAtLogin) Preference() bool                                { return false }
func (*startAtLogin) Set(bool) error                                  { return nil }
func (*startAtLogin) addMenuItem(*application.App, *application.Menu) {}
