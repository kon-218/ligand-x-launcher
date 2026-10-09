package launcher

import (
	"io/fs"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// Run starts the desktop app with the selected frontend and release identity.
func Run(assets fs.FS, publicKey, version string) {
	if err := wails.Run(desktopOptions(assets, publicKey, version)); err != nil {
		println("Error:", err.Error())
	}
}

func desktopOptions(assets fs.FS, publicKey, version string) *options.App {
	// Release tooling injects these values into main; transfer them before the
	// app reads its runtime verification policy.
	runtimeBundlePublicKeyB64 = publicKey
	if version != "" {
		launcherVersion = version
	}
	app := NewApp()

	return &options.App{
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
	}
}
