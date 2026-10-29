//go:build windows

package main

import (
	"context"
	_ "embed"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	winapi "golang.org/x/sys/windows"

	"zapret_gui/internal/config"
	"zapret_gui/internal/winutil"
)

//go:embed build/windows/icon.ico
var trayIcon []byte

func main() {
	noElevate := flag.Bool("no-elevate", false, "не запрашивать права администратора (для разработки)")
	minimized := flag.Bool("minimized", false, "запуск свёрнутым в трей")
	respawn := flag.Bool("watchdog-respawn", false, "внутренний: супервизор перезапуска после watchdog'а трея")
	flag.Parse()

	bootLog("main: старт, admin=%v, args=%v", winutil.IsAdmin(), os.Args)

	// Генератор биндингов wails (wailsbindings.exe) тоже исполняет main()
	// — не элевируем и не мешаем ему.
	isBindingsBinary := strings.Contains(strings.ToLower(os.Args[0]), "wailsbindings")

	// Режим супервизора (запускается умирающим экземпляром через
	// trayWatchdog): окно/трей/wails не поднимаем — только ждём
	// освобождения мьютекса и поднимаем новый экземпляр.
	if *respawn && !isBindingsBinary {
		watchdogRespawnSupervisor()
		return
	}

	// Единственный экземпляр приложения. Мьютекс может короткое время
	// держать стартующий экземпляр-лаунчер (элевация через задачу): окна
	// у него нет — ждём и пробуем ещё раз, а не уходим молча.
	// Единственный экземпляр приложения. Мьютекс может короткое время
	// держать стартующий экземпляр-лаунчер (элевация через задачу): окна
	// у него нет — ждём и пробуем ещё раз, а не уходим молча.
	// Показ окна уже работающего экземпляра — через событие: WinAPI-выовы
	// ShowWindow из чужого процесса у wails-окна НЕ срабатывают (проверено
	// экспериментально) — окно показывает только штатный механизм wails.
	if !isBindingsBinary && !winutil.AcquireSingleInstance("ZapretGui_SingleInstance_Mutex") {
		bootLog("main: уже запущен — прошу показать окно (событие)")
		notifyShowRequest()
		winutil.TryActivateExistingWindow(windowTitle) // подстраховка
		time.Sleep(2 * time.Second)
		if !winutil.AcquireSingleInstance("ZapretGui_SingleInstance_Mutex") {
			bootLog("main: мьютекс всё ещё занят — повтор и выход")
			notifyShowRequest()
			winutil.TryActivateExistingWindow(windowTitle)
			return
		}
	}

	// zapret'у нужен администратор (WinDivert). UAC-запрос должен быть
	// разовым: если задача автозапуска уже создана (админ-запуском) —
	// стартуем elevated-экземпляр через планировщик, без UAC.
	if !isBindingsBinary && !winutil.IsAdmin() && !*noElevate {
		if winutil.TryLaunchElevatedViaTask(config.AppName) {
			return
		}
		var args []string
		if *minimized {
			args = append(args, "-minimized")
		}
		if err := winutil.SelfElevate(args); err != nil {
			println("Не удалось получить права администратора:", err.Error())
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		bootLog("main: ошибка конфигурации: %v", err)
		os.Exit(1)
	}
	if isBindingsBinary {
		bootLog("main: режим генерации биндингов — выходим из мини-логики")
		cfg.StartMinimized = false // биндингам окно не нужно
	}
	if *minimized {
		cfg.StartMinimized = true
	}
	bootLog("main: конфиг загружен, toolsDir=%s", cfg.ToolsDir)
	windowVisible.Store(!cfg.StartMinimized)

	app := NewApp(cfg)

	// Трей (отдельный поток с собственным message loop). Пункты меню — в
	// onReady: до systray.Run трей не инициализирован («tray not ready yet»).
	if !isBindingsBinary {
		go trayLoop(app)
		go trayWatchdog() // «мёртвый значок»: перезапуск, если трей не поднялся
		go watchShowRequest(func() {
			bootLog("main: событие показа окна (повторный запуск exe)")
			if fn, ok := showMainWindow.Load().(func()); ok && fn != nil {
				fn()
			}
		})
	}

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
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.Dark,
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

// showRequestEventName — событие «покажи окно уже работающего экземпляра».
// Повторный запуск exe (второй экземпляр) сеттит его и выходит.
const showRequestEventName = `Local\ZapretGui_ShowWindow`

// notifyShowRequest просит уже запущенный экземпляр показать окно.
func notifyShowRequest() bool {
	k32 := winapi.NewLazySystemDLL("kernel32.dll")
	open := k32.NewProc("OpenEventW")
	set := k32.NewProc("SetEvent")
	n, _ := winapi.UTF16PtrFromString(showRequestEventName)
	h, _, _ := open.Call(uintptr(winapi.EVENT_MODIFY_STATE), 0, uintptr(unsafe.Pointer(n)))
	if h == 0 {
		return false
	}
	defer winapi.CloseHandle(winapi.Handle(h))
	set.Call(h)
	return true
}

// watchShowRequest ждёт событие показа окна (от повторного запуска exe)
// и показывает окно через штатный механизм wails.
func watchShowRequest(show func()) {
	k32 := winapi.NewLazySystemDLL("kernel32.dll")
	create := k32.NewProc("CreateEventW")
	wait := k32.NewProc("WaitForSingleObject")
	n, _ := winapi.UTF16PtrFromString(showRequestEventName)
	h, _, _ := create.Call(0, 0, 0, uintptr(unsafe.Pointer(n)))
	if h == 0 {
		bootLog("watch: CreateEvent не удался")
		return
	}
	for {
		wait.Call(h, ^uintptr(0)) // INFINITE
		show()
	}
}

// trayReady — onReady трея пришёл: иконка добавлена, трей жив.
var trayReady atomic.Bool

// trayLoop — иконка в системном трее (Windows: собственный message loop).
func trayLoop(app *App) {
	bootLog("tray: запуск цикла (поток запинен)")
	systray.Run(func() { // onReady — здесь трей уже инициализирован
		bootLog("tray: onReady — иконка добавлена")
		trayReady.Store(true)
		systray.SetIcon(trayIcon)
		systray.SetTooltip("Zapret GUI — управление zapret и tg-ws-proxy")

		// ЛКМ по иконке — показать/скрыть окно (как в нормальных
		// приложениях); меню — по ПКМ (qa-2 №3)
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
					quitting.Store(true)      // теперь OnBeforeClose пропустит выход
					_ = app.zap.StopProcess() // прибираем запущенные вручную процессы
					systray.Quit()
					if fn, ok := quitApp.Load().(func()); ok && fn != nil {
						fn()
					}
					return
				}
			}
		}()
	}, func() { bootLog("tray: onExit") })
	bootLog("tray: цикл завершён")
}

// trayWatchdog — страховка от «мёртвого значка в трее» (инциденты
// 22.08/23.08): при раннем старте на логоне registerSystray может молча
// сойти (Shell_NotifyIcon до готовности таскбара) — onReady не приходит
// никогда, а message-loop живёт без иконки: пустая ячейка в трее, клики
// не работают, повторный запуск ничего не показывает. Перезапустить
// systray внутри процесса нельзя (синглтон wt, quitOnce, ре-регистрация
// оконного класса упадёт) — перезапускаем приложение целиком через
// задачу автозапуска: свежий процесс строит трей уже при готовом
// таскбаре. Не чаще 3 раз в час (файл-счётчик), иначе цикл рестартов.
func trayWatchdog() {
	const giveUpAfter = 30 * time.Second
	time.Sleep(giveUpAfter)
	if trayReady.Load() {
		return // трей жив
	}
	bootLog("tray: watchdog — onReady не пришёл за %v (значок мёртв?)", giveUpAfter)

	// Окно открыто — приложение доступно, сессию на месте юзера не
	// рубим: ждём, пока он скроет окно (типовой сценарий — автозапуск
	// со свёрнутым окном), проверяем раз в минуту, но не дольше часа.
	for i := 0; i < 60 && !trayReady.Load() && windowVisible.Load(); i++ {
		time.Sleep(time.Minute)
	}
	if trayReady.Load() {
		bootLog("tray: watchdog — трей ожил, отмена")
		return
	}
	if windowVisible.Load() {
		bootLog("tray: watchdog — окно открыто, не перезапускаю (трей мёртв, приложение доступно)")
		return
	}
	if !watchdogRestartAllowed() {
		bootLog("tray: watchdog — лимит перезапусков исчерпан, показываю окно")
		showWindowFallback()
		return
	}
	// Перезапуск возможен только через задачу автозапуска (без UAC):
	// она стартует новый экземпляр с задержкой 4с — к тому моменту мы
	// уже выйдем и освободим мьютекс одиночного экземпляра.
	if _, err := winutil.HiddenCmd("schtasks", "/Query", "/TN", config.AppName).Output(); err != nil {
		bootLog("tray: watchdog — задачи автозапуска нет, показываю окно (%v)", err)
		showWindowFallback()
		return
	}
	watchdogRestartCount()
	// Перезапуск — супервизором (собственный exe c --watchdog-respawn):
	// он наследует админ-токен умирающего экземпляра (без UAC), сам
	// дождётся освобождения мьютекса одиночного экземпляра и поднимет
	// новый экземпляр — сначала задачей автозапуска, при неудаче —
	// напрямую, с ретраями; весь путь пишется в log.txt.
	// Прежний способ (cmd timeout 4 & schtasks /Run) падал вслепую
	// (инцидент 25.08): timeout без консоли не выжидал, а /Run
	// отбрасывался политикой IgnoreNew, пока планировщик считал старый
	// экземпляр живым → приложение погибло на 65 минут.
	exe, exeErr := os.Executable()
	if exeErr != nil {
		bootLog("tray: watchdog — не смог определить exe: %v", exeErr)
		return
	}
	if err := winutil.HiddenCmd(exe, "--watchdog-respawn").Start(); err != nil {
		bootLog("tray: watchdog — супервизор не стартовал: %v", err)
		return
	}
	bootLog("tray: watchdog — супервизор --watchdog-respawn запущен, выходим")
	quitting.Store(true) // OnBeforeClose пропустит выход
	// zapret-службу и tg-ws НЕ трогаем: перезапуск прозрачный, новый
	// экземпляр их подхватит (порт прокси жив — PyInstaller-ребёнок).
	if fn, ok := quitApp.Load().(func()); ok && fn != nil {
		fn()
	}
	time.Sleep(10 * time.Second) // страховка: wails не завершился — выходим жёстко
	bootLog("tray: watchdog — принудительный выход")
	os.Exit(0)
}

// showWindowFallback — трей мёртв, перезапуск невозможен: показываем
// окно, чтобы приложение не оказалось недоступным.
func showWindowFallback() {
	if fn, ok := showMainWindow.Load().(func()); ok && fn != nil {
		fn()
	}
}

// watchdogRestartsPath — файл-счётчик перезапусков watchdog'а в dataDir
// (метки времени через пробел, unix-секунды).
func watchdogRestartsPath() string {
	return filepath.Join(config.DataDir(), "watchdog-restarts.txt")
}

// watchdogRestartAllowed — было ли меньше 3 перезапусков за последний час.
func watchdogRestartAllowed() bool {
	data, _ := os.ReadFile(watchdogRestartsPath())
	now := time.Now().Unix()
	n := 0
	for _, ln := range strings.Fields(string(data)) {
		if ts, err := strconv.ParseInt(ln, 10, 64); err == nil && now-ts < 3600 {
			n++
		}
	}
	return n < 3
}

// watchdogRestartCount — чистит метки старше часа и дописывает текущую.
func watchdogRestartCount() {
	now := time.Now().Unix()
	data, _ := os.ReadFile(watchdogRestartsPath())
	var keep []string
	for _, ln := range strings.Fields(string(data)) {
		if ts, err := strconv.ParseInt(ln, 10, 64); err == nil && now-ts < 3600 {
			keep = append(keep, ln)
		}
	}
	keep = append(keep, strconv.FormatInt(now, 10))
	_ = os.MkdirAll(config.DataDir(), 0o755)
	_ = os.WriteFile(watchdogRestartsPath(), []byte(strings.Join(keep, " ")+"\n"), 0o644)
}

// watchdogRespawnSupervisor — режим `--watchdog-respawn`: detached-процесс,
// поднимающий новый экземпляр после выхода экземпляра, прибитого
// watchdog'ом трея. Запускается умирающим экземпляром ДО его выхода и
// наследует админ-токен, поэтому прямой фолбэк-запуск тоже elevated,
// без UAC. Сам супервизор ничего не держит: ни мьютекса, ни окна, ни
// трея — только опрашивает мьютекс и стартует замену. Весь путь — в
// log.txt (respawn: ...), чтобы подобные инциденты больше не были
// «тихими».
func watchdogRespawnSupervisor() {
	const mutexName = "ZapretGui_SingleInstance_Mutex"
	bootLog("respawn: супервизор запущен, жду освобождения мьютекса")
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) && winutil.SingleInstanceAlive(mutexName) {
		time.Sleep(500 * time.Millisecond)
	}
	if winutil.SingleInstanceAlive(mutexName) {
		// старый экземпляр застрял и не вышел — перезапуск опасен
		bootLog("respawn: старый экземпляр жив спустя 2мин — отмена перезапуска")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		bootLog("respawn: не смог определить exe: %v", err)
		return
	}
	for attempt := 1; attempt <= 3; attempt++ {
		// путь 1: задача автозапуска — стартует elevated без UAC
		out, err := winutil.HiddenCmd("schtasks", "/Run", "/TN", config.AppName).CombinedOutput()
		bootLog("respawn: schtasks /Run (попытка %d): err=%v out=%q",
			attempt, err, strings.TrimSpace(string(out)))
		if instanceCameUp(mutexName, 25*time.Second) {
			bootLog("respawn: новый экземпляр поднят задачей (попытка %d)", attempt)
			return
		}
		// путь 2: прямой запуск — токен супервизора унаследуется
		bootLog("respawn: задача не подняла — прямой запуск (попытка %d)", attempt)
		_ = winutil.HiddenCmd("cmd.exe", "/C", `start "" "`+exe+`"`).Start()
		if instanceCameUp(mutexName, 25*time.Second) {
			bootLog("respawn: новый экземпляр поднят напрямую (попытка %d)", attempt)
			return
		}
		bootLog("respawn: попытка %d не удалась, пауза перед следующей", attempt)
		time.Sleep(5 * time.Second)
	}
	bootLog("respawn: все попытки исчерпаны — приложение не поднято")
}

// instanceCameUp ждёт не дольше timeout, пока кто-нибудь возьмёт мьютекс
// одиночного экземпляра (значит, новый экземпляр стартовал и закрепился).
func instanceCameUp(mutexName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if winutil.SingleInstanceAlive(mutexName) {
			time.Sleep(3 * time.Second) // даём закрепиться; держатель жив — мьютекс жив
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
