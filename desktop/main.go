package main

import (
	"embed"
	_ "embed"
	"log"
	"log/slog"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func mainWindowOptions() application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Title:            "aglink",
		Width:            1100,
		Height:           760,
		MinWidth:         720,
		MinHeight:        480,
		BackgroundColour: application.NewRGB(238, 242, 255),
		URL:              "/",
	}
}

// webviewUserDataPath pins the WebView2 profile (localStorage, cookies, cache)
// to the instance's data directory instead of Wails' default
// %APPDATA%\<binary name>.exe. Two builds of aglink-desktop share that default
// because they share an executable name, which leaks one instance's local
// channel groups and pane layout into the other. Keying on dataDir() ties the
// profile to AGLINK_HOME — the same identity the control token already uses —
// so moving or reinstalling the binary keeps its data, while a separate
// AGLINK_HOME gets a separate profile.
//
// Returns "" on error so Wails falls back to its default rather than failing to
// start; a shared profile is a nuisance, no UI is not.
func webviewUserDataPath() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "webview")
}

func main() {
	// Best-effort: without a log file this GUI-subsystem binary has nowhere to
	// report, but that is no reason to refuse to start.
	var wailsLogger *slog.Logger
	if dir, err := dataDir(); err == nil {
		if f, logger, lerr := setupFileLogging(dir); lerr == nil {
			defer f.Close()
			wailsLogger = logger
		}
	}

	app := application.New(application.Options{
		Name:        "aglink-desktop",
		Description: "aglink desktop frontend",
		// nil would leave Wails logging its system messages to stdout, which a
		// windowsgui binary does not have.
		Logger: wailsLogger,
		Services: []application.Service{
			application.NewService(NewControlService()),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: webviewUserDataPath(),
		},
	})

	app.Window.NewWithOptions(mainWindowOptions())

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
