package main

import (
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// assets and the app* window constants are provided by a build-tag-gated file:
// assets_dev.go (default build, embeds frontend/) or assets_public.go
// (`-tags public`, embeds frontend-public/).

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     appTitle,
		Width:     appWidth,
		Height:    appHeight,
		MinWidth:  appMinWidth,
		MinHeight: appMinHeight,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 3, G: 7, B: 18, A: 1}, // gray-950
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
		Linux: &linux.Options{
			WindowIsTranslucent: false,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
