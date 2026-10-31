//go:build darwin

// app_darwin — реализация App для macOS. Движок DPI-обхода — utunws из
// ZapretMac (Flowseal, официальный порт nfqws): системный перехват через
// pf anchor + туннель utun50, LaunchDaemon от root (см.
// docs/macos-engine-utunws.md). Имена методов совпадают с windows-версией —
// фронтенд один на обе платформы, платформенные секции скрываются по
// status.platform.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"zapret_gui/internal/config"
	"zapret_gui/internal/diag"
	"zapret_gui/internal/engine"
	"zapret_gui/internal/macutil"
	"zapret_gui/internal/release"
	"zapret_gui/internal/strattest"
	"zapret_gui/internal/tgws"
	"zapret_gui/internal/utunws"
)

const AppVersion = "0.2.2-mac.1"

// LaunchAgentLabel — метка автозапуска GUI.
const LaunchAgentLabel = "com.buzmakov.ZapretGui"

// ------------------------------------------------------------------ типы статуса (совместимы с windows)

type StrategyItem struct {
	Name string `json:"name"`
}

type ZapretStatus struct {
	Installed        bool           `json:"installed"`
	Version          string         `json:"version"`
	LatestVersion    string         `json:"latestVersion"`
	Mode             string         `json:"mode"` // service | off
	Running          bool           `json:"running"`
	CurrentStrategy  string         `json:"currentStrategy"`
	AutoRun          bool           `json:"autoRun"`
	Strategies       []StrategyItem `json:"strategies"`
	ServiceState     string         `json:"serviceState"` // на mac всегда ""
	ServiceStrategy  string         `json:"serviceStrategy"`
	GameFilter       string         `json:"gameFilter"`
	Ipset            string         `json:"ipset"`
	CheckUpdatesFlag bool           `json:"checkUpdatesFlag"`
	ProxyOn          bool           `json:"proxyOn"` // совместимость фронта (всегда false)
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
	Platform       string       `json:"platform"`
}

type ProgressEvent struct {
	Task  string  `json:"task"` // zapret | tgws (имена — для прогресс-баров фронта)
	Pct   float64 `json:"pct"`
	Stage string  `json:"stage"`
}

// ------------------------------------------------------------------ App

type App struct {
	ctx      context.Context
	cfg      *config.Config
	ut       *utunws.Manager
	tg       *tgws.Manager
	proxy    *macutil.ProxyManager // только для миграции со SpoofDPI
	logLines []string

	latestMu      sync.Mutex
	latestEngine  string
	latestTgws    string
	latestFetched time.Time

	lastStatusKey string
}

func NewApp(cfg *config.Config) *App {
	a := &App{cfg: cfg}
	a.ut = utunws.New(cfg.ToolsDir, a.pushLog)
	a.tg = tgws.New(cfg.ToolsDir, a.pushLog)
	a.proxy = macutil.NewProxyManager()
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.cfg.StartMinimized {
		runtime.WindowHide(ctx)
	}
	// авто-подъём движка по последней стратегии. Обычно демон уже работает
	// (LaunchDaemon стартует при загрузке сам) — тогда ничего не делаем;
	// если был остановлен — поднимаем (один osascript-запрос пароля).
	if a.ut.Installed() && a.cfg.ZapretAutoRun && a.cfg.StrategyChosen && !a.ut.Running() {
		go func() {
			if err := a.ut.Start(); err != nil {
				bootLog("startup: движок: %v", err)
			}
		}()
	}
	// tg-ws-proxy после перезагрузки сам не поднимется — стартуем сами
	if a.tg.Installed() && a.tg.GetConfig().Autostart && !a.tg.Running() {
		go func() {
			if err := a.tg.Start(); err != nil {
				bootLog("startup: tg-ws-proxy: %v", err)
			}
		}()
	}
	go a.refreshLatest(true)
}

func (a *App) refreshLatest(force bool) {
	a.latestMu.Lock()
	if !force && time.Since(a.latestFetched) < 24*time.Hour {
		a.latestMu.Unlock()
		return
	}
	a.latestMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var eng, tg string
	if tag, _, err := release.LatestZapretMac(ctx); err == nil {
		eng = tag
	}
	if tag, _, err := release.LatestTgwsMac(ctx); err == nil {
		tg = tag
	}

	a.latestMu.Lock()
	if eng != "" || a.latestEngine == "" {
		a.latestEngine = eng
	}
	if tg != "" || a.latestTgws == "" {
		a.latestTgws = tg
	}
	a.latestFetched = time.Now()
	a.latestMu.Unlock()
}

func (a *App) pushLog(line string) {
	a.logLines = append(a.logLines, time.Now().Format("15:04:05")+" "+line)
	if len(a.logLines) > 2000 {
		a.logLines = a.logLines[len(a.logLines)-1500:]
	}
	runtime.EventsEmit(a.ctx, "log", line)
}

func (a *App) emitState() {
	runtime.EventsEmit(a.ctx, "state", nil)
}

// ------------------------------------------------------------------ стратегии: id ↔ название

// strategyIDByTitle — id по названию из GUI (fallback — default).
func (a *App) strategyIDByTitle(title string) string {
	if s, ok := a.ut.StrategyByTitle(title); ok {
		return s.ID
	}
	return utunws.DefaultStrategyID
}

func (a *App) strategyTitleByID(id string) string {
	if s, ok := a.ut.StrategyByID(id); ok {
		return s.Title
	}
	return id
}

// ------------------------------------------------------------------ status

func (a *App) GetStatus() Status {
	s := Status{
		Admin:          macutil.IsRoot(),
		ToolsDir:       a.cfg.ToolsDir,
		AutostartGui:   macutil.AutostartEnabled(LaunchAgentLabel),
		StartMinimized: a.cfg.StartMinimized,
		AppVersion:     AppVersion,
		Platform:       "darwin",
	}

	z := ZapretStatus{
		Installed:       a.ut.Installed(),
		Version:         firstNonEmpty(a.ut.Version(), a.cfg.ZapretVersion),
		CurrentStrategy: a.cfg.LastStrategy,
		AutoRun:         a.cfg.ZapretAutoRun,
		Ipset:           a.ut.IpsetMode(),
	}
	if id := a.ut.SelectedStrategy(); id != "" {
		z.CurrentStrategy = a.strategyTitleByID(id)
	}
	for _, st := range a.ut.Strategies() {
		z.Strategies = append(z.Strategies, StrategyItem{Name: st.Title})
	}
	if z.Installed {
		z.Mode = "off"
		if a.ut.Running() {
			z.Mode = "service"
			z.Running = true
		}
		z.LatestVersion = a.latestEngine
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
		t.LatestVer = a.latestTgws
	}
	s.Tgws = t

	// Первый запуск = не хватает ХОТЯ БЫ ОДНОГО компонента (не только
	// обоих): при миграции с 0.2.1 (spoofdpi→utunws) tgws уже стоит, но
	// движок новый — установка должна запуститься всё равно.
	s.FirstRun = !z.Installed || !t.Installed

	key := fmt.Sprintf("status: engine=%v/%v ipset=%s strategy=%s tgws=running=%v port=%v",
		z.Mode, z.Running, z.Ipset, z.CurrentStrategy, t.Running, t.PortOpen)
	if key != a.lastStatusKey {
		a.lastStatusKey = key
		bootLog("%s", key)
	}
	return s
}

func (a *App) GetLogs() []string {
	out := make([]string, len(a.logLines))
	copy(out, a.logLines)
	return out
}

// ------------------------------------------------------------------ установка/обновление

// EnsureTools скачивает недостающие компоненты (первый запуск).
func (a *App) EnsureTools() error {
	bootLog("ensure-tools: начало (движок=%v tgws=%v)", a.ut.Installed(), a.tg.Installed())
	if !a.ut.Installed() {
		if err := a.migrateFromSpoofDPI(); err != nil {
			a.pushLog("[движок] миграция со spoofdpi: " + err.Error())
		}
		if err := a.installEngine(); err != nil {
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
	a.emitState()
	return nil
}

// migrateFromSpoofDPI — снятие остатков движка v1 (SpoofDPI, прокси-режим):
// выключаем системный прокси (иначе он ломает весь трафик) и глушим
// старый бинарь. Каталог tools/spoofdpi удаляем после успешной установки
// utunws.
func (a *App) migrateFromSpoofDPI() error {
	oldDir := filepath.Join(a.cfg.ToolsDir, "spoofdpi")
	oldBin := filepath.Join(oldDir, "spoofdpi")
	if _, err := os.Stat(oldBin); err == nil {
		a.pushLog("[движок] найден spoofdpi (v1) — останавливаю…")
		macutil.Silent("pkill", "-f", oldBin)
	}
	if macutil.ProxyActiveOn(engine.DefaultPort) {
		if err := a.proxy.ForceProxyOff(); err != nil {
			return fmt.Errorf("не удалось выключить системный прокси: %v", err)
		}
		a.pushLog("[движок] системный прокси spoofdpi выключен")
	} else if st, err := macutil.ReadProxy(); err == nil &&
		(st.HTTPEnable == 1 || st.HTTPSEnable == 1 || st.ProxyAutoConfigEn == 1) {
		// прокси включён, но не наш v1 — не трогаем (настройка юзера)
		a.pushLog("[движок] внимание: в системе включён сторонний прокси — он может мешать обходу")
	}
	return nil
}

func (a *App) installEngine() error {
	emit := func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "zapret", Pct: pct, Stage: stage})
	}
	bootLog("install-engine: запрашиваю релиз ZapretMac…")
	emit(0, "Скачиваю ZapretMac…")
	tag, err := a.ut.Install("", func(pct float64, stage string) {
		emit(pct, stage)
	})
	if err != nil {
		bootLog("install-engine: ошибка: %v", err)
		return err
	}
	a.cfg.ZapretVersion = firstNonEmpty(a.ut.Version(), tag)
	a.cfg.Save()
	a.pushLog("[движок] utunws установлен, версия " + a.cfg.ZapretVersion)
	// старый spoofdpi больше не нужен (код в engine/ оставлен для отката)
	if oldDir := filepath.Join(a.cfg.ToolsDir, "spoofdpi"); oldDir != "" {
		if _, err := os.Stat(oldDir); err == nil {
			_ = os.RemoveAll(oldDir)
			a.pushLog("[движок] каталог spoofdpi удалён (миграция завершена)")
		}
	}
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

// UpdateZapret обновляет движок (переустановка дистрибутива; установщик
// сам перезапускает демон).
func (a *App) UpdateZapret() error {
	if a.ut.Running() {
		// install.sh сам делает bootout/bootstrap — останавливать не нужно
	}
	tag, err := a.ut.Install("", func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "zapret", Pct: pct, Stage: stage})
	})
	if err == nil {
		a.cfg.ZapretVersion = firstNonEmpty(a.ut.Version(), tag)
		a.cfg.Save()
		go a.refreshLatest(true)
	}
	a.emitState()
	return err
}

// ZapretVersions — доступные версии ZapretMac.
func (a *App) ZapretVersions() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tags, err := release.ZapretMacVersions(ctx)
	if err != nil || len(tags) == 0 {
		return []string{}
	}
	return tags
}

// InstallZapretVersion устанавливает конкретную версию ZapretMac.
func (a *App) InstallZapretVersion(tag string) error {
	_, err := a.ut.Install(tag, func(pct float64, stage string) {
		runtime.EventsEmit(a.ctx, "progress", ProgressEvent{Task: "zapret", Pct: pct, Stage: stage})
	})
	if err == nil {
		a.cfg.ZapretVersion = firstNonEmpty(a.ut.Version(), tag)
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

// ------------------------------------------------------------------ движок: управление

// StartZapret запускает движок со стратегией (по названию из GUI).
func (a *App) StartZapret(strategy string) error {
	id := a.strategyIDByTitle(strategy)
	if err := a.ut.SetStrategy(id); err != nil {
		return err
	}
	if err := a.ut.Start(); err != nil {
		return err
	}
	a.cfg.LastStrategy = a.strategyTitleByID(id)
	a.cfg.StrategyChosen = true // явный запуск — разрешаем автоподъём
	a.cfg.Save()
	a.emitState()
	return nil
}

func (a *App) StopZapret() error {
	if err := a.ut.Stop(); err != nil {
		return err
	}
	a.emitState()
	return nil
}

// StopZapretService — эквивалент StopZapret (демон один).
func (a *App) StopZapretService() error { return a.StopZapret() }

// InstallZapretService — на macOS «служба» = запущенный LaunchDaemon.
func (a *App) InstallZapretService(strategy string) error { return a.StartZapret(strategy) }

// RemoveZapretService — остановка движка (автозапуск при загрузке тоже
// снимается — так работает stop.sh ZapretMac).
func (a *App) RemoveZapretService() error { return a.StopZapret() }

func (a *App) SetZapretAutoRun(on bool) error {
	a.cfg.ZapretAutoRun = on
	return a.cfg.Save()
}

// SetGameFilter — GameFilter из win-сборки в ZapretMac не переносится;
// сегмент скрыт в UI, здесь — заглушка.
func (a *App) SetGameFilter(mode string) error { return nil }

// SetZapretCheckUpdates — win-концепт; на mac скрыт.
func (a *App) SetZapretCheckUpdates(on bool) error { return nil }

// SetIpset — режим ipset (none/any/loaded) + применение при работающем
// движке (перезапуск демона — один osascript-запрос пароля).
func (a *App) SetIpset(mode string) error {
	if err := a.ut.SetIpsetMode(mode); err != nil {
		return err
	}
	a.pushLog("[движок] ipset: " + mode)
	if a.ut.Running() {
		if err := a.ut.Start(); err != nil { // restart.sh
			return err
		}
	}
	a.emitState()
	return nil
}

// UpdateIpset обновляет списки из дистрибутива (аналог win-кнопки ⟳).
func (a *App) UpdateIpset() error {
	n, err := a.ut.RefreshLists()
	if err != nil {
		return err
	}
	a.pushLog(fmt.Sprintf("[движок] списки обновлены (%d файлов)", n))
	if a.ut.Running() {
		if err := a.ut.Start(); err != nil {
			return err
		}
	}
	a.emitState()
	return nil
}

// SelectStrategy выбирает стратегию (и перезапускает движок, если работает).
func (a *App) SelectStrategy(strategy string) error {
	id := a.strategyIDByTitle(strategy)
	if err := a.ut.SetStrategy(id); err != nil {
		return err
	}
	a.cfg.LastStrategy = a.strategyTitleByID(id)
	a.cfg.StrategyChosen = true // явный выбор — разрешаем автоподъём
	a.cfg.Save()
	if a.ut.Running() {
		if err := a.ut.Start(); err != nil {
			return err
		}
	}
	a.emitState()
	return nil
}

// OpenOriginalServiceBat/OpenOriginalStrategyBat — терминальный менеджер
// дистрибутива zapret (только Windows).
func (a *App) OpenOriginalServiceBat() error {
	return fmt.Errorf("терминальный менеджер доступен только на Windows")
}

func (a *App) OpenOriginalStrategyBat(name string) error {
	return fmt.Errorf("терминальный запуск стратегий доступен только на Windows")
}

func (a *App) OpenZapretFolder() error {
	// пользовательские данные движка: списки/стратегия — здесь
	macutil.OpenFolder(utunws.DataRoot())
	return nil
}
func (a *App) OpenTgwsFolder() error { macutil.OpenFolder(a.tg.Dir()); return nil }

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

// OldTgwsAutostart — на macOS старых автозапусков не было.
func (a *App) OldTgwsAutostart() string { return "" }

func (a *App) RemoveOldTgwsAutostart() error { return nil }

func (a *App) TgwsLink() string { return a.tg.Link() }

// OpenTgwsInTelegram открывает tg://proxy для автонастройки Telegram.
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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return macutil.SetAutostart(LaunchAgentLabel, exe, on)
}

func (a *App) SetStartMinimized(on bool) error {
	a.cfg.StartMinimized = on
	a.cfg.Save()
	return nil
}

// SetupDefenderExclusion — Windows Defender существует только на Windows;
// на macOS кнопка скрыта в UI, здесь заглушка для единого биндинга.
func (a *App) SetupDefenderExclusion() (string, error) {
	return "", fmt.Errorf("доступно только на Windows")
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
	return diag.Run(a.ut)
}

// ------------------------------------------------------------------ тест стратегий

var testRunning atomic.Bool

// TestUrl проверяет доступность одного URL при текущем состоянии.
func (a *App) TestUrl(url string) strattest.UrlTestResult {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	r := strattest.TestUrl(url, 8*time.Second)
	verdict := "ОШИБКА: " + r.Err
	if r.Ok {
		verdict = fmt.Sprintf("OK %d", r.Status)
	} else if r.Err == "" {
		verdict = fmt.Sprintf("ПЛОХОЙ ОТВЕТ %d", r.Status)
	}
	a.pushLog(fmt.Sprintf("[test] %s → %s (%d мс)", r.Url, verdict, r.Ms))
	return r
}

// StartStrategyTest — полный прогон стратегий utunws. Привилегированный
// цикл (смена selected-strategy + kickstart) идёт ОДНИМ osascript-запросом
// пароля (маркер-файлы, см. utunws.TestLoop), URL-тесты выполняет GUI.
func (a *App) StartStrategyTest(customUrl string) error {
	if !a.ut.Installed() {
		return fmt.Errorf("движок не установлен")
	}
	if !testRunning.CompareAndSwap(false, true) {
		return fmt.Errorf("тест уже выполняется")
	}

	wasRunning := a.ut.Running()
	lastID := firstNonEmpty(a.ut.SelectedStrategy(), utunws.DefaultStrategyID)
	lastTitle := a.strategyTitleByID(lastID)
	customHost := ""
	if customUrl != "" {
		customHost = hostOf(customUrl)
	}

	go func() {
		defer testRunning.Store(false)

		urls := strattest.DefaultUrls
		if customHost != "" {
			urls = []string{"https://" + customHost + "/generate_204"}
		}

		strategies := a.ut.Strategies()
		ids := make([]string, len(strategies))
		for i, s := range strategies {
			ids[i] = s.ID
		}

		loop, err := a.ut.StartTestLoop(ids, lastID, wasRunning)
		if err != nil {
			a.pushLog("[test] не удалось запустить привилегированный цикл: " + err.Error())
			return
		}

		suite := strattest.Suite{Results: []strattest.StrategyResult{}}
		done := 0
		for {
			id, ok := loop.Next(3 * time.Minute)
			if !ok {
				break
			}
			title := a.strategyTitleByID(id)
			runtime.EventsEmit(a.ctx, "test-progress", map[string]interface{}{
				"strategy": title, "done": done, "total": len(ids),
			})
			res := strattest.StrategyResult{Strategy: title, Total: len(urls)}
			if why := loop.FailWhy(id); why != "" {
				a.pushLog("[test] «" + title + "»: движок не поднялся (" + why + ")")
				res.Total = 0
				suite.Results = append(suite.Results, res)
				loop.Done(id)
				done++
				continue
			}
			var sumMs int64
			for _, u := range urls {
				r := strattest.TestUrl(u, 8*time.Second)
				if r.Ok {
					res.Ok++
				}
				sumMs += r.Ms
			}
			if res.Total > 0 {
				res.AvgMs = sumMs / int64(res.Total)
			}
			a.pushLog(fmt.Sprintf("[test] %s → %d/%d OK (%d мс)", title, res.Ok, res.Total, res.AvgMs))
			suite.Results = append(suite.Results, res)
			loop.Done(id)
			done++
		}

		if err := loop.Wait(); err != nil {
			a.pushLog("[test] привилегированный цикл: " + err.Error())
		}

		suite.Recommended = strattest.Recommend(suite.Results, lastTitle)

		// восстанавливаем состояние (root-скрипт уже вернул стратегию)
		if wasRunning {
			suite.RestoredMode = "service"
		} else {
			suite.RestoredMode = "off"
		}

		runtime.EventsEmit(a.ctx, "test-done", suite)
		a.emitState()
	}()
	return nil
}

// hostOf возвращает хост из URL.
func hostOf(rawURL string) string {
	s := rawURL
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := indexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}

// ------------------------------------------------------------------ служебное

func (a *App) ReportFrontendError(msg string) {
	a.pushLog("[js-error] " + msg)
	bootLog("js-error: %s", msg)
}

func (a *App) FrontendPing() {
	bootLog("frontend: живой")
}

func (a *App) IsAdmin() bool { return macutil.IsRoot() }
