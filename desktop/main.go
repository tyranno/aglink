package main

import (
	"embed"
	_ "embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func mainWindowOptions() application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Title:            "teleclaude",
		Width:            1100,
		Height:           760,
		MinWidth:         720,
		MinHeight:        480,
		BackgroundColour: application.NewRGB(238, 242, 255),
		URL:              "/",
	}
}

func main() {
	app := application.New(application.Options{
		Name:        "aglink-desktop",
		Description: "teleclaude desktop frontend",
		Services: []application.Service{
			application.NewService(NewControlService()),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(mainWindowOptions())

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
