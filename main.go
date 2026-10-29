package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"zapret_gui/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

// Колбэки, доступные трею до/после инициализации wails-контекста.
var showMainWindow atomic.Value // func()
var hideWindow atomic.Value     // func()
var quitApp atomic.Value        // func()

// windowVisible — видимо ли окно (для ЛКМ-переключения из трея).
// Отслеживаем сами: Wails v2 не даёт WindowIsVisible.
var windowVisible atomic.Bool

// toggleMainWindow — ЛКМ по иконке в трее: показать/скрыть окно (qa-2 №3).
func toggleMainWindow() {
	bootLog("tray: ЛКМ по иконке, windowVisible=%v", windowVisible.Load())
	if windowVisible.Load() {
		if fn, ok := hideWindow.Load().(func()); ok && fn != nil {
			fn()
		}
		return
	}
	if fn, ok := showMainWindow.Load().(func()); ok && fn != nil {
		fn()
	}
}

// quitting — настоящий выход (меню трея «Выход»), а не сворачивание
// крестиком. Без этого флага OnBeforeClose перехватил бы и выход из трея:
// иконка уже удалена (systray.Quit), окно скрыто, а процесс остаётся жить.
var quitting atomic.Bool

const windowTitle = "Zapret GUI"

// bootLog пишет строки в лог бутстрапа (dataDir/log.txt).
var bootLogMu sync.Mutex

func bootLog(format string, args ...interface{}) {
	_ = os.MkdirAll(config.DataDir(), 0o755)
	line := fmt.Sprintf("%s %s\n", timeStamp(), fmt.Sprintf(format, args...))
	bootLogMu.Lock()
	defer bootLogMu.Unlock()
	f, err := os.OpenFile(logFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line)
}

func logFilePath() string {
	return config.DataDir() + string(os.PathSeparator) + "log.txt"
}

func timeStamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// hideToTrayHandler — общий OnBeforeClose: крестик сворачивает в трей.
func hideToTrayHandler(ctx context.Context, hide func()) bool {
	if quitting.Load() {
		return false // настоящий выход
	}
	hide()
	windowVisible.Store(false) // крестик скрыл окно — трей об этом знает
	return true
}
