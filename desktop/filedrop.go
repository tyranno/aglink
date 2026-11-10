package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// setupFileDrop forwards files dropped onto a chat pane to the frontend as
// "files:dropped" {files, paneId}. The pane element carries data-pane-id so
// the files attach to the pane they were dropped on, not the focused one.
func setupFileDrop(app *application.App, win *application.WebviewWindow) {
	win.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		ctx := e.Context()
		files := ctx.DroppedFiles()
		if len(files) == 0 {
			return
		}
		paneID := ""
		if d := ctx.DropTargetDetails(); d != nil {
			paneID = d.Attributes["data-pane-id"]
		}
		app.Event.Emit("files:dropped", map[string]any{"files": files, "paneId": paneID})
	})
}
