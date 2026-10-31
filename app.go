//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"zapret_gui/internal/config"
	"zapret_gui/internal/diag"
	"zapret_gui/internal/netwatch"
	"zapret_gui/internal/release"
	"zapret_gui/internal/strattest"
	"zapret_gui/internal/tgws"
	"zapret_gui/internal/winutil"
	"zapret_gui/internal/zapret"
)

const AppVersion = "0.2.9.1"

// ------------------------------------------------------------------ типы статуса (для фронтенда)

type StrategyItem struct {
	Name string `json:"name"`
}

type ZapretStatus struct {
	Installed        bool           `json:"installed"`
	Version          string         `json:"version"`
	LatestVersion    string         `json:"latestVersion"`
	Mode             string         `json:"mode"` // service | process | off
	Running          bool           `json:"running"`
	CurrentStrategy  string         `json:"currentStrategy"`
	AutoRun          bool           `json:"autoRun"` // авто-подъём при старте GUI
	Strategies       []StrategyItem `json:"strategies"`
	ServiceState     string         `json:"serviceState"`
	ServiceStrategy  string         `json:"serviceStrategy"`
	GameFilter       string         `json:"gameFilter"`
	Ipset            string         `json:"ipset"`
	CheckUpdatesFlag bool           `json:"checkUpdatesFlag"`
	VpnPause         bool           `json:"vpnPause"`    // автопауза zapret при VPN включена
	VpnActive        bool           `json:"vpnActive"`   // VPN несёт интернет (маршрут по умолчанию в туннеле)
	VpnName          string         `json:"vpnName"`     // имя туннельного адаптера
	VpnAdapters      []string       `json:"vpnAdapters"` // поднятые туннельные адаптеры (справочно)
}

type TgwsStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	LatestVer string `json:"latestVersion"`
	Running   bool   `json:"running"`
	PortOpen  bool   `json:"portOpen"`
	Link      string `json:"link"`
	Autostart bool   `json:"autostart"`
}

type Status struct {
	Admin          bool         `json:"admin"`
	FirstRun       bool         `json:"firstRun"`
	ToolsDir       string       `json:"toolsDir"`
	Zapret         ZapretStatus `json:"zapret"`
	Tgws           TgwsStatus   `json:"tgws"`
	AutostartGui   bool         `json:"autostartGui"`
	StartMinimized bool         `json:"startMinimized"`
	AppVersion     string       `json:"appVersion"`
	Platform       string       `json:"platform"` // windows | darwin
}

type ProgressEvent struct {
	Task  string  `json:"task"` // zapret | tgws
	Pct   float64 `json:"pct"`
	Stage string  `json:"stage"`
}

// ------------------------------------------------------------------ App

type App struct {
	ctx      context.Context
	cfg      *config.Config
	zap      *zapret.Manager
	tg       *tgws.Manager
	logMu    sync.Mutex // logLines пишут несколько горутин (zapret, тест, автопроверка)
	logLines []string

	latestMu      sync.Mutex
	latestZapret  string
	latestTgws    string
	latestFetched time.Time

	lastStatusKey string // диф-диагностика GetStatus
	lastZapState  string // переходы состояния zapret для журнала GUI
}

func NewApp(cfg *config.Config) *App {
	a := &App{cfg: cfg}
	// режим автолечения известен сразу: фронт спрашивает GetAutoHeal при загрузке,
	// ещё до того как startup() поднимет наблюдение
	a.setAutoHealDryRun(cfg.AutoHealDryRun)
	a.zap = zapret.New(filepath.Join(cfg.ToolsDir, "zapret"), a.pushLog)
	a.tg = tgws.New(cfg.ToolsDir, a.pushLog)
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.cfg.StartMinimized {
		runtime.WindowHide(ctx)
	}
	if a.zap.Installed() {
		a.zap.EnsureUserLists() // пользовательские списки должны существовать
	}
	// Диагностика состояния IPSet: режим «any» (пустой ipset-all.txt) означает,
	// что правила winws с --ipset применяются к ЛЮБОМУ IP. Это агрессивный
	// обход всего трафика — он ломает чужие прокси (например, tg-ws: его
	// TLS-соединения к Cloudflare сервер молча дропает). Пишем в log.txt,
	// чтобы причина «одно работает, другое нет» была видна без археологии.
	if a.zap.Installed() && a.zap.IpsetStatus() == "any" {
		bootLog("startup: IPSet в режиме «any» (пустой список) — правила winws применяются к любому IP; " +
			"если ломается tg-ws/Telegram или Cloudflare-сайты, смените стратегию или добавьте IP в lists/ipset-exclude-user.txt")
	}

	// Автозапуск — задача планировщика (RunLevel Highest): при входе в
	// систему GUI стартует уже админом, без UAC-запроса. Пересоздаём её
	// при каждом старте — так автоматически мигрируется старая запись
	// HKCU Run и лечится переезд exe на новое место.
	if a.cfg.AutostartGui && winutil.IsAdmin() {
		if err := winutil.SetAutostart(config.AppName, true); err != nil {
			bootLog("startup: задача автозапуска: %v", err)
		}
	}
	// zapret: как и tg-ws-proxy, поднимаем автоматически — по последним
	// сохранённым параметрам (стратегия; gamefilter/ipset — файлы дистрибутива).
	// Через службу: она стартует и при загрузке Windows, ещё до входа.
	// НО: только после первого явного выбора стратегии (qa-2 №2) — на
	// первом запуске юзер ещё не подобрал стратегию под свою сеть.
	vpnHold := a.vpnStartupHold() // VPN уже несёт интернет — обход не поднимаем
	if !vpnHold && a.zap.Installed() && a.cfg.ZapretAutoRun && a.cfg.StrategyChosen {
		go func() {
			if err := a.zap.EnsureRunning(a.cfg.LastStrategy); err != nil {
				bootLog("startup: zapret: %v", err)
			}
		}()
	}
	// tg-ws-proxy: прокси — не служба, после перезагрузки его никто не
	// поднимет. Если у него в конфиге включён автозапуск — стартуем сами.
	// На раннем логоне сеть/WFP могут быть ещё не готовы и прокси умирает,
	// не открыв порт: делаем до 3 попыток с нарастающей паузой (10с, 20с).
	if a.tg.Installed() && a.tg.GetConfig().Autostart && !a.tg.Running() {
		go func() {
			delay := 0 * time.Second
			for attempt := 1; attempt <= 3; attempt++ {
				time.Sleep(delay)
				if a.tg.Running() && a.tg.PortOpen() {
					return // уже поднят (например, юзером из GUI)
				}
				if err := a.tg.Start(); err == nil {
					bootLog("startup: tg-ws-proxy: запущен (попытка %d)", attempt)
					return
				} else {
					bootLog("startup: tg-ws-proxy: попытка %d: %v", attempt, err)
				}
				delay += 10 * time.Second
			}
		}()
	}
	// Автолечение доменов (0.2.6.0): пассивное наблюдение за браузером.
	// По умолчанию — «только журнал»; лечение включается в настройках.
	// Запускаем после autostart/zapret: снифферу нужен админ и инструменты.
	if vpnHold {
		a.setAutoWatchErr("пауза: интернет идёт через VPN")
	} else {
		a.startAutoWatch()
	}
	a.startVpnWatch()
	go a.refreshLatest(true)
}

// refreshLatest обновляет кэш версий с GitHub (раз в сутки).
func (a *App) refreshLatest(force bool) {
	a.latestMu.Lock()
	if !force && time.Since(a.latestFetched) < 24*time.Hour {
		a.latestMu.Unlock()
		return
	}
	a.latestMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var zap, tg string
	if tag, _, err := release.LatestZapret(ctx); err == nil {
		zap = tag
	}
	if tag, _, err := release.LatestTgws(ctx); err == nil {
		tg = tag
	}

	a.latestMu.Lock()
	if zap != "" || a.latestZapret == "" {
		a.latestZapret = zap
	}
	if tg != "" || a.latestTgws == "" {
		a.latestTgws = tg
	}
	a.latestFetched = time.Now()
	a.latestMu.Unlock()
}

func (a *App) pushLog(line string) {
	a.logMu.Lock()
	a.logLines = append(a.logLines, time.Now().Format("15:04:05")+" "+line)
	if len(a.logLines) > 2000 {
		a.logLines = a.logLines[len(a.logLines)-1500:]
	}
	a.logMu.Unlock()
	// ctx == nil в headless-тестах бэкенда (без Wails-рантайма)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "log", line)
	}
}

func (a *App) emitState() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "state", nil)
	}
}

// ------------------------------------------------------------------ status

func (a *App) GetStatus() Status {
	s := Status{
		Admin:          winutil.IsAdmin(),
		ToolsDir:       a.cfg.ToolsDir,
		AutostartGui:   winutil.AutostartEnabled(config.AppName),
		StartMinimized: a.cfg.StartMinimized,
		AppVersion:     AppVersion,
		Platform:       "windows",
	}

	z := ZapretStatus{
		Installed:        a.zap.Installed(),
		Version:          firstNonEmpty(a.zap.Version(), a.cfg.ZapretVersion),
		GameFilter:       a.zap.GameFilterMode(),
		Ipset:            a.zap.IpsetStatus(),
		CheckUpdatesFlag: a.zap.CheckUpdatesEnabled(),
		CurrentStrategy:  a.cfg.LastStrategy,
		AutoRun:          a.cfg.ZapretAutoRun,
	}
	if z.Installed {
		if list, err := zapret.ScanStrategies(a.zap.Dir()); err == nil {
			for _, st := range list {
				z.Strategies = append(z.Strategies, StrategyItem{Name: st.Name})
			}
		}
		if svcState, _ := zapret.ServiceState(); svcState != "" {
			z.ServiceState = svcState
			z.ServiceStrategy = zapret.ServiceStrategy()
			if svcState == "RUNNING" {
				z.Mode = "service"
				z.Running = true
				z.CurrentStrategy = z.ServiceStrategy
			}
		}
		if a.zap.ProcessRunning() {
			z.Mode = "process"
			z.Running = true
		}
		if z.Mode == "" {
			z.Mode = "off"
		}
		z.LatestVersion = a.latestZapret // кэш, см. refreshLatest
	}
	// VPN: автопауза zapret (0.2.7.0)
	z.VpnPause = a.cfg.PauseOnVpn
	if vi := a.vpnInfo(); true {
		z.VpnActive = vi.Active
		z.VpnName = vi.Name
		z.VpnAdapters = vi.Adapters
	}
	s.Zapret = z

	t := TgwsStatus{
		Installed: a.tg.Installed(),
		Version:   firstNonEmpty(a.tg.Version(), a.cfg.TgwsVersion),
		Running:   a.tg.Running(),
		PortOpen:  a.tg.PortOpen(),
		Link:      a.tg.Link(),
		Autostart: a.tg.GetConfig().Autostart,
	}
	if t.Installed {
		t.LatestVer = a.latestTgws // кэш, см. refreshLatest
	}
	s.Tgws = t

	s.FirstRun = !z.Installed && !t.Installed

	// диф-диагностика: пишем в bootLog только при изменении ключевых статусов
	key := fmt.Sprintf("status: zapret=%s/%v tgws=running=%v port=%v", s.Zapret.Mode, s.Zapret.Running, s.Tgws.Running, s.Tgws.PortOpen)
	if key != a.lastStatusKey {
		a.lastStatusKey = key
		bootLog("%s", key)
	}
	// переходы состояния zapret — в журнал GUI: пользователь должен видеть,
	// что «Запустить» действительно подняло обход (или не смогло)
	zapKey := fmt.Sprintf("%s/%v/%s", s.Zapret.Mode, s.Zapret.Running, s.Zapret.CurrentStrategy)
	if zapKey != a.lastZapState {
		a.lastZapState = zapKey
		switch {
		case s.Zapret.Running && s.Zapret.Mode == "service":
			a.pushLog("[zapret] работает: служба Windows · " + s.Zapret.CurrentStrategy)
		case s.Zapret.Running:
			a.pushLog("[zapret] работает: процесс · " + s.Zapret.CurrentStrategy)
		case s.Zapret.Installed:
			a.pushLog("[zapret] остановлен")
		}
	}
	return s
}

func (a *App) GetLogs() []string {
	// всегда непустой слайс: nil сериализуется в null и ломает итерацию на фронте
	a.logMu.Lock()
	defer a.logMu.Unlock()
	out := make([]string, len(a.logLines))
	copy(out, a.logLines)
	return out
}

// ------------------------------------------------------------------ установка/обновление

// EnsureTools скачивает недостающие компоненты (первый запуск).
func (a *App) EnsureTools() error {
	bootLog("ensure-tools: начало (zapret=%v tgws=%v)", a.zap.Installed(), a.tg.Installed())
	if !a.zap.Installed() {
		if err := a.installZapret(); err != nil {
			return err
		}
	}
	if !a.tg.Installed() {
		tag, err := a.tg.Install(func(pct float64, stage string) {
			runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "tgws", Pct: pct, Stage: stage})
		})
		if err != nil {
			return err
		}
		a.cfg.TgwsVersion = tag
		a.cfg.Save()
	}
	bootLog("ensure-tools: завершено")
	// исключения Defender — сразу после скачивания, до первого запуска
	// движка: иначе WinDivert может улететь в карантин (qa-2 №1).
	// Асинхронно: PowerShell холодный старт ~2с × 2 вызова — не вешаем
	// модалку первого запуска; движок на первом запуске всё равно не
	// стартует (StrategyChosen=false), торопиться некуда.
	go func() {
		if _, err := a.SetupDefenderExclusion(); err != nil {
			bootLog("ensure-tools: defender: %v", err)
		}
	}()
	a.emitState()
	return nil
}

// SetupDefenderExclusion добавляет каталоги приложения (tools и папку
// самого exe) в исключения Windows Defender. Требует прав администратора —
// они у нас и так есть (WinDivert). Возвращает человекочитаемый итог.
func (a *App) SetupDefenderExclusion() (string, error) {
	if !winutil.IsAdmin() {
		return "", fmt.Errorf("нужны права администратора")
	}
	paths := []string{a.cfg.ToolsDir}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Dir(exe))
	}
	var added []string
	for _, p := range paths {
		if p == "" || winutil.DefenderExclusionActive(p) {
			continue
		}
		if err := winutil.AddDefenderExclusion(p); err != nil {
			a.pushLog("[defender] не удалось: " + err.Error())
			return "", err
		}
		added = append(added, p)
	}
	if len(added) == 0 {
		a.pushLog("[defender] исключения уже на месте")
		return "исключения уже настроены", nil
	}
	a.pushLog("[defender] добавлены исключения: " + strings.Join(added, "; "))
	return "исключения добавлены (Defender больше не тронет файлы)", nil
}

func (a *App) installZapret() error {
	emit := func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "zapret", Pct: pct, Stage: stage})
	}
	bootLog("install-zapret: запрашиваю релиз…")
	emit(0, "Запрашиваю последний релиз…")
	ctx := context.Background()
	tag, asset, err := release.LatestZapret(ctx)
	if err != nil {
		bootLog("install-zapret: ошибка релиза: %v", err)
		return err
	}
	bootLog("install-zapret: релиз %s, ассет %s", tag, asset.Name)
	tmpZip := filepath.Join(a.cfg.ToolsDir, "zapret-update.zip")
	emit(0, "Скачиваю zapret…")
	if err := release.Download(ctx, asset.URL, tmpZip, func(sent, total int64) {
		if total > 0 {
			emit(float64(sent)/float64(total)*0.8, "Скачиваю zapret…")
		}
	}); err != nil {
		return err
	}
	emit(0.85, "Распаковываю…")
	dir := filepath.Join(a.cfg.ToolsDir, "zapret")
	if err := release.Unzip(tmpZip, dir); err != nil {
		bootLog("install-zapret: ошибка распаковки: %v", err)
		return err
	}
	a.zap.EnsureUserLists() // списки нужны winws с первого запуска
	emit(0.95, "Готово")
	// в релизном zip нет .service/version.txt — берём версию дистрибутива
	a.cfg.ZapretVersion = firstNonEmpty(a.zap.Version(), tag)
	a.cfg.Save()
	a.pushLog("[zapret] установлен, версия " + a.cfg.ZapretVersion)
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// UpdateZapret обновляет дистрибутив zapret с сохранением настроек.
func (a *App) UpdateZapret() error {
	a.autoWatchPause()
	defer func() {
		// файлы дистрибутива заменены — старый WinDivert.dll в памяти больше
		// не соответствует новому драйверу, при следующем открытии нужен свежий
		netwatch.Unload()
		a.autoWatchResume(3 * time.Second)
	}()
	ver, err := a.zap.Update(func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "zapret", Pct: pct, Stage: stage})
	})
	if err == nil {
		a.cfg.ZapretVersion = ver
		a.cfg.Save()
		go a.refreshLatest(true)
	}
	a.emitState()
	return err
}

// ZapretVersions — список доступных версий zapret (новейшие первыми).
func (a *App) ZapretVersions() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tags, err := release.ZapretVersions(ctx)
	if err != nil || len(tags) == 0 {
		return []string{} // nil ломает итерацию на фронте
	}
	return tags
}

// InstallZapretVersion устанавливает конкретную версию zapret.
func (a *App) InstallZapretVersion(tag string) error {
	a.autoWatchPause()
	defer func() {
		netwatch.Unload()
		a.autoWatchResume(3 * time.Second)
	}()
	ver, err := a.zap.UpdateTo(tag, func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "zapret", Pct: pct, Stage: stage})
	})
	if err == nil {
		a.cfg.ZapretVersion = ver
		a.cfg.Save()
	}
	a.emitState()
	return err
}

// UpdateTgws обновляет tg-ws-proxy.
func (a *App) UpdateTgws() error {
	tag, err := a.tg.Update(func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "tgws", Pct: pct, Stage: stage})
	})
	if err == nil {
		a.cfg.TgwsVersion = tag
		a.cfg.Save()
		go a.refreshLatest(true)
	}
	a.emitState()
	return err
}

// ------------------------------------------------------------------ zapret: управление

// waitServiceRunning ждёт, пока служба реально станет RUNNING. `sc start`
// отвечает «запускается», а winws может упасть через секунду (например,
// застрявший WinDivert-драйвер) — раньше в этом случае показывали «служба
// запущена», а лампа не горела.
func waitServiceRunning(timeout time.Duration) error {
	start := time.Now()
	for {
		st, _ := zapret.ServiceState()
		switch st {
		case "RUNNING":
			return nil
		case "STOPPED", "":
			if time.Since(start) < 1500*time.Millisecond {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			code, _ := zapret.ServiceExitCode()
			if st == "" {
				return errors.New("служба пропала из SCM сразу после старта")
			}
			return fmt.Errorf("служба не удержалась: код %d — %s", code, serviceExitHint(code))
		}
		if time.Since(start) > timeout {
			return fmt.Errorf("служба не запустилась за %s (состояние %s)", timeout, st)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// serviceExitHint — человеческое объяснение кода выхода winws.
func serviceExitHint(code uint32) string {
	switch code {
	case 433: // ERROR_DEVICE_DOES_NOT_EXIST
		return "WinDivert-драйвер недоступен (частое последствие обновления zapret на работающем GUI) — перезапустите приложение"
	case 1067: // ERROR_PROCESS_ABORTED
		return "winws завершился сразу после старта — проверьте журнал и стратегию"
	case 0:
		return "winws завершился сразу после старта"
	}
	return "winws завершился сразу после старта"
}

// serviceStartVerified ставит/стартует службу и проверяет, что она жива.
// Сниффер на время операции отпускает WinDivert-драйвер: иначе драйвер может
// застрять в STOP_PENDING, и winws не сможет открыть устройство.
func (a *App) serviceStartVerified(strategy string) error {
	a.autoWatchPause()
	defer a.autoWatchResume(2 * time.Second)
	err := a.zap.ServiceInstall(strategy)
	if err == nil {
		err = waitServiceRunning(8 * time.Second)
	}
	if err != nil {
		// вторая попытка: драйвер мог освобождаться дольше обычного
		netwatch.Unload()
		time.Sleep(500 * time.Millisecond)
		if e2 := a.zap.ServiceInstall(strategy); e2 == nil {
			if e3 := waitServiceRunning(8 * time.Second); e3 == nil {
				a.pushLog("[zapret] служба запущена со второй попытки")
				err = nil
			} else {
				err = e3
			}
		} else {
			err = e2
		}
	}
	if err != nil {
		a.pushLog("[zapret] служба не запустилась: " + err.Error())
	}
	return err
}
func (a *App) StartZapret(strategy string) error {
	a.vpnManualStartWarning()
	a.autoWatchPause()
	defer a.autoWatchResume(2 * time.Second)
	if err := a.zap.Start(strategy); err != nil {
		return err
	}
	a.cfg.LastStrategy = strategy
	a.cfg.StrategyChosen = true // явный запуск — разрешаем автоподъём
	a.cfg.Save()
	a.emitState()
	return nil
}

func (a *App) StopZapret() error {
	a.autoWatchPause()
	defer a.autoWatchResume(2 * time.Second)
	return a.stopZapretInternal()
}

// stopZapretInternal — остановка без паузы/возобновления сниффера.
func (a *App) stopZapretInternal() error {
	// останавливаем то, что работает: службу или процесс
	if svcState, _ := zapret.ServiceState(); svcState == "RUNNING" || svcState == "START_PENDING" {
		if err := zapret.ServiceStop(); err != nil {
			a.pushLog("[zapret] остановка службы: " + err.Error())
			return err
		}
		a.pushLog("[zapret] служба остановлена")
	}
	if err := a.zap.StopProcess(); err != nil {
		return err
	}
	a.emitState()
	return nil
}

// RestartZapret перезапускает zapret со стратегией strategy (пустая — последняя
// выбранная): службу — если она установлена, иначе фоновый процесс.
// Нужен после правки пользовательских списков: winws читает их только на старте.
func (a *App) RestartZapret(strategy string) error {
	a.autoWatchPause()
	defer a.autoWatchResume(2 * time.Second)
	if !a.zap.Installed() {
		return errors.New("zapret не установлен")
	}
	if strategy == "" {
		strategy = a.cfg.LastStrategy
	}
	svcInstalled := false
	if st, _ := zapret.ServiceState(); st != "" {
		svcInstalled = true // служба есть в SCM (даже остановленная)
	}
	if err := a.stopZapretInternal(); err != nil {
		return err
	}
	if svcInstalled {
		// EnsureRunning поднимет службу (net start или пересоздание при
		// смене стратегии) и сам дефолтит пустую стратегию на general
		if err := a.zap.EnsureRunning(strategy); err != nil {
			return err
		}
		a.pushLog("[zapret] служба перезапущена")
	} else {
		if strategy == "" {
			strategy = a.defaultStrategy()
		}
		if strategy == "" {
			return errors.New("сначала выберите стратегию")
		}
		if err := a.zap.Start(strategy); err != nil {
			return err
		}
		a.pushLog("[zapret] процесс перезапущен (стратегия «" + strategy + "»)")
	}
	if strategy != "" {
		a.cfg.LastStrategy = strategy
		a.cfg.StrategyChosen = true
		a.cfg.Save()
	}
	a.emitState()
	return nil
}

// defaultStrategy — «general», иначе первая найденная стратегия.
func (a *App) defaultStrategy() string {
	list, err := zapret.ScanStrategies(a.zap.Dir())
	if err != nil {
		return ""
	}
	for _, s := range list {
		if s.Name == "general" {
			return s.Name
		}
	}
	if len(list) > 0 {
		return list[0].Name
	}
	return ""
}

// GetUserDomains возвращает пользовательские домены для GUI: ключи
// general (маскировать), exclude (исключить), ipset-exclude (IP/CIDR).
// Комментарии и заглушки дистрибутива не показываются — юзер видит
// только свои записи.
func (a *App) GetUserDomains() (map[string][]string, error) {
	return a.zap.GetUserDomains()
}

// SetUserDomains сохраняет пользовательские домены по kind'ам (только
// переданные ключи). Пустой список безопасен: пишется заглушка, zapret
// продолжает работать. Применение — перезапуском zapret.
func (a *App) SetUserDomains(domains map[string][]string) error {
	if err := a.zap.SetUserDomains(domains); err != nil {
		return err
	}
	a.pushLog("[zapret] пользовательские списки сохранены")
	a.emitState()
	return nil
}

// StopZapretService останавливает службу, не удаляя её.
func (a *App) StopZapretService() error {
	a.autoWatchPause()
	defer a.autoWatchResume(2 * time.Second)
	zapret.ServiceStop()
	a.emitState()
	return nil
}

func (a *App) InstallZapretService(strategy string) error {
	a.vpnManualStartWarning()
	if err := a.serviceStartVerified(strategy); err != nil {
		return err
	}
	a.cfg.LastStrategy = strategy
	a.cfg.StrategyChosen = true // явный запуск — разрешаем автоподъём
	a.cfg.Save()
	a.emitState()
	return nil
}

func (a *App) RemoveZapretService() error {
	a.autoWatchPause()
	defer a.autoWatchResume(2 * time.Second)
	if err := zapret.ServiceRemove(); err != nil {
		return err
	}
	a.emitState()
	return nil
}

func (a *App) SetZapretAutoRun(on bool) error {
	a.cfg.ZapretAutoRun = on
	return a.cfg.Save()
}

func (a *App) SetGameFilter(mode string) error {
	err := a.zap.SetGameFilter(mode)
	a.emitState()
	return err
}

func (a *App) SetIpset(mode string) error {
	err := a.zap.SetIpset(mode)
	a.emitState()
	return err
}

func (a *App) UpdateIpset() error { return a.zap.UpdateIpset() }

func (a *App) SetZapretCheckUpdates(on bool) error {
	err := a.zap.SetCheckUpdates(on)
	a.emitState()
	return err
}

// SelectStrategy выбирает стратегию (и перезапускает процесс, если он работает).
func (a *App) SelectStrategy(strategy string) error {
	a.cfg.LastStrategy = strategy
	a.cfg.StrategyChosen = true // явный выбор — разрешаем автоподъём
	a.cfg.Save()
	if a.zap.ProcessRunning() {
		if err := a.zap.Start(strategy); err != nil {
			return err
		}
	}
	a.emitState()
	return nil
}

// OpenOriginalServiceBat открывает оригинальный консольный менеджер zapret.
func (a *App) OpenOriginalServiceBat() error {
	return winutil.RunBatVisible(a.zap.Dir(), "service.bat")
}

// OpenOriginalStrategyBat открывает bat стратегии в консоли (как в терминале).
func (a *App) OpenOriginalStrategyBat(name string) error {
	list, err := zapret.ScanStrategies(a.zap.Dir())
	if err != nil {
		return err
	}
	for _, s := range list {
		if s.Name == name {
			return winutil.RunBatVisible(a.zap.Dir(), filepath.Base(s.File))
		}
	}
	return fmt.Errorf("стратегия %q не найдена", name)
}

func (a *App) OpenZapretFolder() error { winutil.OpenFolder(a.zap.Dir()); return nil }
func (a *App) OpenTgwsFolder() error   { winutil.OpenFolder(a.tg.Dir()); return nil }

// ------------------------------------------------------------------ tgws: управление

func (a *App) StartTgws() error {
	if err := a.tg.Start(); err != nil {
		return err
	}
	a.emitState()
	return nil
}

func (a *App) StopTgws() error {
	if err := a.tg.Stop(); err != nil {
		return err
	}
	a.emitState()
	return nil
}

func (a *App) SetTgwsAutostart(on bool) error {
	err := a.tg.SetAutostart(on)
	a.emitState()
	return err
}

// OldTgwsAutostart — путь старого автозапуска tg-ws-proxy (или "").
func (a *App) OldTgwsAutostart() string {
	return winutil.OldTgwsAutostartPath()
}

// RemoveOldTgwsAutostart убирает старый автозапуск (C:\zapret\…),
// чтобы после перезагрузки не появлялась вторая иконка в трее.
func (a *App) RemoveOldTgwsAutostart() error {
	if err := winutil.RemoveOldTgwsAutostart(); err != nil {
		return err
	}
	a.pushLog("[tg-ws] старый автозапуск удалён (HKCU Run\\TgWsProxy)")
	a.emitState()
	return nil
}

func (a *App) TgwsLink() string { return a.tg.Link() }

// OpenTgwsInTelegram открывает ссылку tg://proxy для автонастройки Telegram.
func (a *App) OpenTgwsInTelegram() error {
	link := a.tg.Link()
	if link == "" {
		return fmt.Errorf("ссылка недоступна — сначала запустите tg-ws-proxy")
	}
	runtime.BrowserOpenURL(a.ctx, link)
	return nil
}

// ------------------------------------------------------------------ настройки приложения

func (a *App) SetAutostartGui(on bool) error {
	a.cfg.AutostartGui = on
	a.cfg.Save()
	return winutil.SetAutostart(config.AppName, on)
}

func (a *App) SetStartMinimized(on bool) error {
	a.cfg.StartMinimized = on
	a.cfg.Save()
	return nil
}

func (a *App) GetAppConfig() map[string]interface{} {
	return map[string]interface{}{
		"toolsDir":       a.cfg.ToolsDir,
		"autostartGui":   a.cfg.AutostartGui,
		"startMinimized": a.cfg.StartMinimized,
	}
}

func (a *App) ShowWindow() {
	runtime.WindowUnminimise(a.ctx)
	runtime.Show(a.ctx)
}

// ------------------------------------------------------------------ диагностика

func (a *App) RunDiagnostics() []diag.Check {
	bin := ""
	if a.zap.Installed() {
		bin = filepath.Join(a.zap.Dir(), "bin")
	}
	return diag.Run(bin)
}

// ------------------------------------------------------------------ тест стратегий

var testRunning atomic.Bool

// TestUrl проверяет доступность одного URL при текущем состоянии zapret.
func (a *App) TestUrl(url string) strattest.UrlTestResult {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	r := strattest.TestUrl(url, 8*time.Second)
	a.pushLog(fmt.Sprintf("[test] %s → %s (%d мс)", r.Url, testVerdict(r), r.Ms))
	return r
}

func testVerdict(r strattest.UrlTestResult) string {
	if r.Ok {
		return fmt.Sprintf("OK %d", r.Status)
	}
	if r.Err != "" {
		return "ОШИБКА: " + r.Err
	}
	return fmt.Sprintf("ПЛОХОЙ ОТВЕТ %d", r.Status)
}

// host возвращает «youtube.com» из URL — для читабельности строк теста.
func host(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// StartStrategyTest запускает полный прогон всех стратегий в фоне.
// Используется ОРИГИНАЛЬНЫЙ тестовый скрипт дистрибутива (test zapret.ps1,
// режим DPI-чекеров TCP 16-20 по ~36 внешним хостам) — тот же прогон, что
// и через терминал, с теми же итогами: OK/FAIL/BLOCKED и «Best strategy».
// Результаты приходят событиями: test-progress {strategy, done, total} и
// test-done {Suite}. Исходное состояние (служба) восстанавливается.
func (a *App) StartStrategyTest(customUrl string) error {
	if !a.zap.Installed() {
		return fmt.Errorf("zapret не установлен")
	}
	// автопроверка домена тоже останавливает обход — не мешаем друг другу
	if autoDomainRunning.Load() {
		return fmt.Errorf("идёт автопроверка домена — дождитесь её окончания")
	}
	if !testRunning.CompareAndSwap(false, true) {
		return fmt.Errorf("тест уже выполняется")
	}
	// и повторная проверка после захвата: автопроверка могла стартовать
	// между первой проверкой и захватом флага
	if autoDomainRunning.Load() {
		testRunning.Store(false)
		return fmt.Errorf("идёт автопроверка домена — дождитесь её окончания")
	}

	// снимок текущего состояния: скрипт требует, чтобы служба zapret
	// была снята (иначе откажется работать)
	wasService := false
	wasServiceStrategy := ""
	if st, _ := zapret.ServiceState(); st != "" {
		wasService = true
		wasServiceStrategy = zapret.ServiceStrategy()
		if err := zapret.ServiceRemove(); err != nil {
			testRunning.Store(false)
			return fmt.Errorf("не удалось снять службу для теста: %v", err)
		}
		a.pushLog("[test] служба снята на время прогона — верну после теста")
	}
	lastStrategy := firstNonEmpty(a.cfg.LastStrategy, wasServiceStrategy)
	customHost := zapret.HostOf(customUrl)

	go func() {
		defer testRunning.Store(false)

		termResults, best, err := a.zap.RunTerminalTest(customHost,
			func(line string) { a.pushLog("[test] " + line) },
			func(done, total int, strategy string) {
				runtime.EventsEmit(a.ctx, "test-progress", map[string]interface{}{
					"strategy": strategy, "done": done, "total": total,
				})
			})

		suite := strattest.Suite{Results: []strattest.StrategyResult{}}
		for _, tr := range termResults {
			suite.Results = append(suite.Results, strattest.StrategyResult{
				Strategy: tr.Name,
				Ok:       tr.Ok,
				Total:    tr.Ok + tr.Fail,
				Blocked:  tr.Blocked,
			})
		}
		suite.Recommended = best
		if err != nil {
			a.pushLog("[test] ошибка прогона: " + err.Error())
		}

		// восстанавливаем исходное состояние: службу (как и было) или процесс
		_ = a.zap.StopProcess()
		switch {
		case wasService:
			if err := a.zap.ServiceInstall(firstNonEmpty(wasServiceStrategy, suite.Recommended, "general")); err == nil {
				suite.RestoredMode = "service"
			} else {
				a.pushLog("[test] не удалось вернуть службу: " + err.Error())
			}
		case lastStrategy != "" && !a.cfg.ZapretAutoRun:
			if err := a.zap.Start(lastStrategy); err == nil {
				suite.RestoredMode = "process"
			}
		default:
			suite.RestoredMode = "off"
		}

		runtime.EventsEmit(a.ctx, "test-done", suite)
		a.emitState()
	}()
	return nil
}

// ------------------------------------------------------------------ служебное

// ReportFrontendError принимает ошибки JS-фронтенда в журнал.
func (a *App) ReportFrontendError(msg string) {
	a.pushLog("[js-error] " + msg)
	bootLog("js-error: %s", msg)
}

// FrontendPing — маркер «фронтенд загрузился» (диагностика бутстрапа).
func (a *App) FrontendPing() {
	bootLog("frontend: живой")
}

// IsAdmin — выставляется фронтенду для баннера.
func (a *App) IsAdmin() bool { return winutil.IsAdmin() }
