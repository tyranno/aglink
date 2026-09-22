package main

import (
	"embed"
	_ "embed"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func mainWindowOptions() application.WebviewWindowOptions {
	// Restore the saved size / maximised state over the defaults.
	return applyWindowState(application.WebviewWindowOptions{
		Title:            "aglink",
		Width:            1100,
		Height:           760,
		MinWidth:         720,
		MinHeight:        480,
		BackgroundColour: application.NewRGB(238, 242, 255),
		URL:              "/",
	})
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
			WebviewUserDataPath:   webviewUserDataPath(),
			AdditionalBrowserArgs: devtoolsArgs(),
		},
	})

	loadWindowState()
	win := app.Window.NewWithOptions(mainWindowOptions())
	// Persist the window size / maximised state so a large view is restored next
	// launch. Save on resize and on close (close covers the case where the last
	// change was a maximise/restore).
	saveGeo := func(*application.WindowEvent) { captureWindowGeometry(win) }
	win.OnWindowEvent(events.Common.WindowDidResize, saveGeo)
	win.OnWindowEvent(events.Common.WindowClosing, saveGeo)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// devtoolsArgs opens a Chrome DevTools Protocol port on the WebView2 window when
// AGLINK_WEBVIEW_DEBUG_PORT is set, so aglink-web can drive this UI as text
// (profile "app:aglink" or "cdp:<port>") instead of by screen capture. Unset —
// every normal launch and every shipped build — it returns nothing and no port
// is opened.
//
// It has to be an app option: Wails' WebView2 loader deliberately blanks the
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS environment variable, so the usual
// outside-in way of passing --remote-debugging-port is ignored. Copy this
// function into any other Wails app to make it drivable the same way.
func devtoolsArgs() []string {
	if p := os.Getenv("AGLINK_WEBVIEW_DEBUG_PORT"); p != "" {
		return []string{"--remote-debugging-port=" + p}
	}
	return nil
}
