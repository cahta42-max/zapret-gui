//go:build darwin

package main

import (
	"context"
	_ "embed"
	"flag"
	"os"
	"runtime/debug"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"zapret_gui/internal/config"
	"zapret_gui/internal/macutil"
)

//go:embed build/appicon.png
var trayIcon []byte

// trayTemplate — отдельная иконка для строки меню: только глиф «Z»,
// чёрный на прозрачном (template). Иконка приложения не подходит:
// она на тёмной плитке и в режиме template выглядит чёрным квадратом.
//
//go:embed build/tray_template.png
var trayTemplate []byte

func main() {
	minimized := flag.Bool("minimized", false, "запуск свёрнутым в трей")
	flag.Parse()

	bootLog("main: старт (macOS), args=%v", os.Args)
	defer func() { // паника — в лог-файл (GUI-приложение молчит иначе)
		if r := recover(); r != nil {
			bootLog("PANIC: %v\n%s", r, debug.Stack())
		}
	}()
	macutil.WriteStartupStamp()

	// Единственный экземпляр (flock). Второй запуск — активируем окно первого.
	if rel, ok := macutil.AcquireSingleInstance("ZapretGui-single.lock"); !ok {
		macutil.ActivateExistingApp("ZapretGui")
		return
	} else {
		defer rel()
	}

	cfg, err := config.Load()
	if err != nil {
		bootLog("main: ошибка конфигурации: %v", err)
		os.Exit(1)
	}
	if *minimized {
		cfg.StartMinimized = true
	}
	bootLog("main: конфиг загружен, toolsDir=%s", cfg.ToolsDir)
	windowVisible.Store(!cfg.StartMinimized)

	app := NewApp(cfg)

	// Трей на macOS: NSStatusItem можно создавать ТОЛЬКО в главном потоке
	// («NSWindow should only be instantiated on the main thread»). Wails
	// вызывает OnStartup в ГОРУТИНЕ (не в главном потоке) — инициализировать
	// трей из OnStartup нельзя (краш в nativeStart). Поэтому трей стартуем
	// здесь, на главной горутине (она привязана к главному потоку через
	// runtime.LockOSThread в init-е пакета systray), ДО wails.Run:
	// пункты меню systray отложит в главный runloop, который поднимет
	// wails.Run. Возвращает функцию очистки иконки на выходе.
	trayEnd := initTray(app)
	defer trayEnd()

	err = wails.Run(&options.App{
		Title:            windowTitle,
		Width:            820,
		Height:           600,
		MinWidth:         640,
		MinHeight:        360,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 14, G: 17, B: 22, A: 1},
		StartHidden:      cfg.StartMinimized,
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			bootLog("wails: startup")
			// клик по иконке Dock при скрытом окне — показываем окно
			// (у wails нет applicationShouldHandleReopen, см. dock_reopen_darwin.go)
			installDockReopenHandler()
			showMainWindow.Store(func() {
				runtime.WindowUnminimise(ctx)
				runtime.WindowShow(ctx)
				windowVisible.Store(true)
			})
			hideWindow.Store(func() {
				runtime.WindowHide(ctx)
				windowVisible.Store(false)
			})
			quitApp.Store(func() { runtime.Quit(ctx) })
		},
		OnBeforeClose: func(ctx context.Context) bool {
			return hideToTrayHandler(ctx, func() { runtime.WindowHide(ctx) })
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   "Zapret GUI",
				Message: "Управление utunws (zapret) и tg-ws-proxy (macOS)",
			},
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		bootLog("wails: ошибка: %v", err)
		println("Error:", err.Error())
		os.Exit(1)
	}
	bootLog("main: выход")
}

// initTray создаёт иконку в строке меню. Вызывать ТОЛЬКО из главной
// горутины (главный поток) — см. комментарий в main. Возвращает функцию
// очистки трея на выходе.
func initTray(app *App) (end func()) {
	start, end := systray.RunWithExternalLoop(func() { // onReady
		// шаблонная иконка: только чёрный Z на прозрачном — macOS сам
		// перекрашивает под светлую/тёмную тему меню-бара
		systray.SetTemplateIcon(trayTemplate, trayTemplate)
		systray.SetTooltip("Zapret GUI — управление utunws (zapret) и tg-ws-proxy")

		// ЛКМ по иконке — показать/скрыть окно; меню — по ПКМ
		systray.SetOnTapped(toggleMainWindow)

		mShow := systray.AddMenuItem("Открыть", "Показать окно")
		mQuit := systray.AddMenuItem("Выход", "Завершить работу")

		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					if fn, ok := showMainWindow.Load().(func()); ok && fn != nil {
						fn()
					}
				case <-mQuit.ClickedCh:
					// настоящий выход: завершаем приложение. Движок-демон
					// (utunws/LaunchDaemon) НЕ останавливаем — он системный и
					// продолжает работать без GUI (как служба на Windows)
					quitting.Store(true)
					systray.Quit()
					if fn, ok := quitApp.Load().(func()); ok && fn != nil {
						fn()
					}
					return
				}
			}
		}()
	}, func() { /* onExit — сам end() в defer main приберёт иконку */ })
	start() // ГЛАВНЫЙ поток: здесь создаётся NSStatusItem (до wails.Run)
	return end
}
