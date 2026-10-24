package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// Version 是程序版本号的唯一来源。
//
// 发布流程：改这里的版本号 → 同步 CHANGELOG.md → 打同名 tag（如 v0.2.0）。
// 自动更新检查拿它和 GitHub 最新 release 的 tag 比较；cmd/probe 与构建脚本
// 也读它，任何地方都不要另写字面量版本号。
var Version = "0.1.0"

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "套图工作台",
		Width:     1280,
		Height:    860,
		MinWidth:  1060,
		MinHeight: 700,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 16, G: 16, B: 18, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			Theme:                windows.Dark,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
