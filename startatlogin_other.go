//go:build !linux && !darwin

package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// startAtLogin is Linux and macOS only for now. A nil one leaves the tray
// item and the wizard's checkbox out.
type startAtLogin struct{}

func newStartAtLogin(string, *log.Logger) *startAtLogin { return nil }

func (*startAtLogin) Supported() bool               { return false }
func (*startAtLogin) Preference() bool              { return false }
func (*startAtLogin) Set(bool) error                { return nil }
func (*startAtLogin) addMenuItem(*application.Menu) {}
