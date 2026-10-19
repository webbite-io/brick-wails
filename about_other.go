//go:build !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// setupAboutWindow is a no-op outside macOS, where the app has no app menu
// to hold an About item.
func setupAboutWindow(*application.App) {}
