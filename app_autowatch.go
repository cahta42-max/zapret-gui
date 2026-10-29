//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"zapret_gui/internal/domaincheck"
	"zapret_gui/internal/netwatch"
	"zapret_gui/internal/winutil"
)

// Автолечение: пассивно смотрим TLS-соединения браузера и, если сайт
// стабильно не открывается, сами прогоняем проверку «с обходом/без» и
// записываем домен в нужный список. Два режима:
//   - журнал (dryRun, по умолчанию после обновления): только пишем в журнал,
//     списки и обход не трогаем — так калибруется детектор;
//   - лечение: применяем, как ручная кнопка «Проверить и добавить».
//
// Перед лечением делаем «тихий» замер с обходом (без остановки трафика):
// если сайт с обходом открывается — это был разовый сбой, списки не трогаем.

const (
	autoHealFlowTimeout = 5 * time.Second
	// сколько после лечения не верим событиям детектора: после рестарта
	// обхода браузер переподключается, эти обрывы — не «сбой домена»
	autoHealSettleAfterHeal = 20 * time.Second
	// одно и то же событие в журнал не чаще, чем раз в 2 минуты
	autoHealLogGap = 2 * time.Minute
	// пауза домену, если лечение не понадобилось/не удалось
	autoHealShortCooldown = 30 * time.Minute
)

// snifferHolder — текущий сниффер под мьютексом: цикл ретраев может его
// заменить, а closer-горутина читает — без гонки.
type snifferHolder struct {
	mu sync.Mutex
	s  *netwatch.Sniffer
}

func (h *snifferHolder) get() *netwatch.Sniffer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.s
}

func (h *snifferHolder) set(s *netwatch.Sniffer) {
	h.mu.Lock()
	h.s = s
	h.mu.Unlock()
}

func (h *snifferHolder) close() {
	if s := h.get(); s != nil {
		s.Close()
	}
}

var (
	autoWatchMu     sync.Mutex
	autoWatchCancel context.CancelFunc
	autoWatchActive bool
	autoWatchGen    uint64 // номер поколения: старый cleanup не должен гасить новый сниффер
	autoWatchPaused bool   // техническая пауза (старт/стоп/обновление zapret)
	autoWatchErr    string // почему наблюдение не запущено (для честного статуса в UI)

	autoHealPolicyMu sync.Mutex
	autoHealPolicy   *netwatch.Policy

	// режим читается из горутины сниффера, пишется из UI — под мьютексом
	autoHealModeMu sync.RWMutex
	autoHealDryRun bool

	// события детектора после лечения игнорируем до этого времени (unix nano)
	autoHealSettleUntil atomic.Int64

	autoHealLogMu sync.Mutex
	autoHealLast  = map[string]time.Time{}
)

func (a *App) setAutoWatchErr(s string) {
	autoWatchMu.Lock()
	autoWatchErr = s
	autoWatchMu.Unlock()
}

func (a *App) isAutoWatchActive() bool {
	autoWatchMu.Lock()
	defer autoWatchMu.Unlock()
	return autoWatchActive
}

func (a *App) autoHealDryRunNow() bool {
	autoHealModeMu.RLock()
	defer autoHealModeMu.RUnlock()
	return autoHealDryRun
}

func (a *App) setAutoHealDryRun(v bool) {
	autoHealModeMu.Lock()
	autoHealDryRun = v
	autoHealModeMu.Unlock()
}

// startAutoWatch вызывается при старте приложения: поднимает наблюдение,
// если оно включено в настройках и есть права администратора.
func (a *App) startAutoWatch() {
	// режим держим синхронным с конфигом даже если наблюдение не поднимется:
	// иначе UI покажет «лечить автоматически» при конфиге «только журнал»
	a.setAutoHealDryRun(a.cfg.AutoHealDryRun)
	if !a.cfg.AutoHealEnabled {
		return
	}
	if !winutil.IsAdmin() {
		a.setAutoWatchErr("нужны права администратора")
		a.pushLog("[автолечение] наблюдение не запущено: нужны права администратора")
		bootLog("autowatch: нет прав администратора — наблюдение не запущено")
		return
	}
	a.autoWatchStart()
}

// autoWatchStart открывает сниффер и запускает конвейер детектор/политика.
func (a *App) autoWatchStart() {
	autoWatchMu.Lock()
	if autoWatchActive {
		autoWatchMu.Unlock()
		return
	}
	if !a.zap.Installed() {
		autoWatchMu.Unlock()
		a.setAutoWatchErr("zapret не установлен")
		a.pushLog("[автолечение] наблюдение не запущено: zapret не установлен")
		return
	}
	dllDir := filepath.Join(a.cfg.ToolsDir, "zapret", "bin")
	snf, err := netwatch.OpenSniffer(dllDir, netwatch.DefaultFilter)
	if err != nil {
		autoWatchMu.Unlock()
		a.setAutoWatchErr(err.Error())
		a.pushLog("[автолечение] наблюдение не запущено: " + err.Error())
		bootLog("autowatch: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	autoWatchGen++
	gen := autoWatchGen
	autoWatchCancel = cancel
	autoWatchActive = true
	autoWatchMu.Unlock()

	a.setAutoHealDryRun(a.cfg.AutoHealDryRun)
	a.setAutoWatchErr("") // запустилось успешно
	det := netwatch.NewDetector(autoHealFlowTimeout)
	det.OnEvent = a.onTrafficEvent
	autoHealPolicyMu.Lock()
	autoHealPolicy = netwatch.NewPolicy()
	autoHealPolicyMu.Unlock()

	a.pushLog(fmt.Sprintf("[автолечение] наблюдение за браузером включено (режим: %s)", a.autoHealModeLabel()))
	bootLog("autowatch: включено (%s)", a.autoHealModeLabel())

	hold := &snifferHolder{}
	hold.set(snf)

	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					det.Tick()
				}
			}
		}()

		var firstPacket, firstHello atomic.Bool
		handler := func(outbound bool, raw []byte) {
			p, ok := netwatch.ParsePacket(raw)
			if !ok {
				return
			}
			// одноразовая диагностика в log.txt: видит ли сниффер трафик и
			// разбирается ли SNI на реальных ClientHello
			if firstPacket.CompareAndSwap(false, true) {
				bootLog("autowatch: сниффер видит трафик (%s:%d → %s:%d)",
					p.SrcIP, p.SrcPort, p.DstIP, p.DstPort)
			}
			if outbound && !firstHello.Load() {
				if host, isCH := netwatch.SNIHost(p.Payload); isCH && host != "" {
					if firstHello.CompareAndSwap(false, true) {
						bootLog("autowatch: первый ClientHello — %s", host)
					}
				}
			}
			det.OnPacket(p, outbound)
		}

		// WinDivert может отвалиться (обновление, сон/пробуждение, ошибка
		// драйвера) — пробуем поднять сниффер заново, не теряя наблюдение
		for attempt := 1; attempt <= 3 && ctx.Err() == nil; attempt++ {
			err := hold.get().RunWithContext(ctx, handler)
			if ctx.Err() != nil {
				break
			}
			bootLog("autowatch: сниффер остановился (попытка %d): %v", attempt, err)
			if attempt < 3 {
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
				if ctx.Err() != nil {
					break
				}
				if next, oerr := netwatch.OpenSniffer(dllDir, netwatch.DefaultFilter); oerr == nil {
					hold.set(next)
					a.pushLog("[автолечение] сниффер перезапущен")
					continue
				} else {
					bootLog("autowatch: перезапуск сниффера: %v", oerr)
				}
			}
			break
		}
		hold.close()

		autoWatchMu.Lock()
		// старый экземпляр не должен трогать состояние нового (быстрый off→on)
		if autoWatchGen == gen {
			autoWatchActive = false
			autoWatchCancel = nil
			autoWatchErr = "сниффер остановился — нажмите «перезапустить наблюдение»"
		}
		autoWatchMu.Unlock()
	}()

	// ждём отмены: только владелец поколения закрывает сниффер
	go func() {
		<-ctx.Done()
		hold.close()
	}()
}

// stopAutoWatch закрывает сниффер. announce — писать ли в журнал «выключено»
// (пользователь выключил в настройках) или тихо (техническая пауза).
func (a *App) stopAutoWatch(announce bool) {
	autoWatchMu.Lock()
	cancel := autoWatchCancel
	autoWatchCancel = nil
	autoWatchActive = false
	autoWatchMu.Unlock()
	if cancel != nil {
		cancel()
		if announce {
			a.pushLog("[автолечение] наблюдение выключено")
		}
	}
}

// autoWatchStop останавливает наблюдение (пользователь выключил в настройках).
func (a *App) autoWatchStop() {
	autoWatchMu.Lock()
	autoWatchPaused = false // пользователь выключил — отложенный resume не нужен
	autoWatchMu.Unlock()
	a.stopAutoWatch(true)
}

// autoWatchPause временно отпускает WinDivert-драйвер. Пока сниффер держит
// хэндл, драйвер нельзя остановить или заменить: «net stop WinDivert»
// (обновление дистрибутива zapret) повисает в STOP_PENDING, а следующий
// winws падает с ошибкой 433 «device does not exist» — снаружи это выглядит
// как «нажал Запустить, а лампа не горит».
func (a *App) autoWatchPause() {
	autoWatchMu.Lock()
	autoWatchPaused = true
	active := autoWatchActive
	autoWatchMu.Unlock()
	if !active {
		return
	}
	a.stopAutoWatch(false)
	// ждём фактического закрытия хэндла (WinDivertClose) — до 3 с
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		autoWatchMu.Lock()
		still := autoWatchActive
		autoWatchMu.Unlock()
		if !still {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	bootLog("autowatch: хэндл сниффера не закрылся за 3 с")
}

// autoWatchResume возвращает наблюдение после технической паузы (если оно
// включено в настройках). after — небольшая задержка, чтобы служба успела
// поднять WinDivert-драйвер.
func (a *App) autoWatchResume(after time.Duration) {
	if after <= 0 {
		a.autoWatchResumeNow()
		return
	}
	go func() {
		time.Sleep(after)
		a.autoWatchResumeNow()
	}()
}

// autoWatchResumeNow — немедленная часть autoWatchResume.
func (a *App) autoWatchResumeNow() {
	autoWatchMu.Lock()
	paused := autoWatchPaused
	autoWatchPaused = false
	autoWatchMu.Unlock()
	if paused && a.cfg.AutoHealEnabled {
		a.autoWatchStart()
	}
}

// autoHealModeLabel — что сейчас делает автолечение (для журнала и UI).
func (a *App) autoHealModeLabel() string {
	if a.autoHealDryRunNow() {
		return "только журнал"
	}
	return "лечить автоматически"
}

// autoHealLogRate — не чаще одного сообщения по домену в 2 минуты.
func (a *App) autoHealLogRate(domain, line string) {
	now := time.Now()
	autoHealLogMu.Lock()
	last, ok := autoHealLast[domain]
	if ok && now.Sub(last) < autoHealLogGap {
		autoHealLogMu.Unlock()
		return
	}
	autoHealLast[domain] = now
	autoHealLogMu.Unlock()
	a.pushLog(line)
	bootLog("autowatch: %s", line)
}

// onTrafficEvent — событие детектора. Быстро, без блокировки чтения пакетов.
func (a *App) onTrafficEvent(e netwatch.Event) {
	// Пока идёт прогон (ручной или авто) — свои же неудачные замеры не считаем:
	// иначе автолечение начинает реагировать на собственную проверку.
	if autoDomainRunning.Load() || testRunning.Load() {
		return
	}
	// После лечения браузер переподключается — это не «сбой домена»
	if time.Now().UnixNano() < autoHealSettleUntil.Load() {
		return
	}
	autoHealPolicyMu.Lock()
	pol := autoHealPolicy
	var domain string
	var due bool
	if pol != nil {
		domain, due = pol.Observe(e)
	}
	autoHealPolicyMu.Unlock()

	if e.Kind == netwatch.KindHealthy {
		return
	}
	a.autoHealLogRate(e.Domain,
		fmt.Sprintf("[автолечение] %s: %s (%s)", e.Domain, e.Kind, e.Detail))
	if !due {
		return
	}
	if a.autoHealDryRunNow() {
		// решение — отдельная строка без rate-limit: иначе dry-run не даёт
		// калибровочной картины (лимит глушил бы её вторым вызовом на домен)
		a.pushLog("[автолечение] " + domain + ": сработал бы автопрогон (журнал: списки не меняю)")
		bootLog("autowatch: dry-run решение по %s", domain)
		return
	}
	go a.healDomain(domain)
}

// healDomain — автоматический прогон «с обходом/без» с записью списка.
func (a *App) healDomain(domain string) {
	// проба и рестарт обхода — не «сбои домена»: окно успокоения с самого начала
	autoHealSettleUntil.Store(time.Now().Add(autoHealSettleAfterHeal).UnixNano())
	defer func() {
		if p := recover(); p != nil {
			a.pushLog(fmt.Sprintf("[автолечение] паника: %v", p))
			bootLog("autowatch: паника в лечении: %v", p)
		}
		autoHealSettleUntil.Store(time.Now().Add(autoHealSettleAfterHeal).UnixNano())
	}()

	// сначала «тихий» замер с обходом — без остановки трафика: чаще всего
	// повторный замер уже проходит, и лечить нечего
	probe := a.probeUrl("https://" + domain + "/")
	if domaincheck.Reachable(probe) {
		a.autoHealLogRate(domain, "[автолечение] "+domain+
			": с обходом открывается — похоже на разовый сбой, списки не меняю")
		a.shortCooldown(domain, autoHealShortCooldown)
		return
	}

	a.pushLog("[автолечение] " + domain + ": сайт нестабилен и с обходом — проверяю (обход кратко выключится)")
	res, err := a.CheckAndAddDomain(domain)
	if err != nil {
		a.pushLog("[автолечение] " + domain + ": не удалось — " + err.Error())
		a.shortCooldown(domain, autoHealShortCooldown)
		return
	}
	msg := res.Message
	if res.Applied {
		suffix := " (список обновлён"
		if res.MovedFrom != "" {
			suffix += ", убран из «" + res.MovedFrom + "»"
		}
		if res.Restarted {
			suffix += ", обход перезапущен"
		}
		msg += suffix + ")"
	}
	if res.Applied && !res.Healed {
		msg += " — но сайт так и не открылся"
	}
	a.pushLog("[автолечение] " + domain + ": " + msg)
	bootLog("autowatch: лечение %s: applied=%v restarted=%v healed=%v", domain, res.Applied, res.Restarted, res.Healed)
	if !res.Applied {
		// ничего не поменяли — не жжём полный 6-часовой кулдаун
		a.shortCooldown(domain, autoHealShortCooldown)
	}
	a.emitState()
}

// shortCooldown — пауза домену после бесполезной/неудачной попытки.
func (a *App) shortCooldown(domain string, d time.Duration) {
	autoHealPolicyMu.Lock()
	if autoHealPolicy != nil {
		autoHealPolicy.ForceCooldown(domain, d)
	}
	autoHealPolicyMu.Unlock()
}

// ------------------------------------------------------------------ биндинги для UI

// AutoHealStatus — состояние автолечения для интерфейса.
type AutoHealStatus struct {
	Enabled   bool   `json:"enabled"`
	DryRun    bool   `json:"dryRun"`
	Active    bool   `json:"active"`
	Mode      string `json:"mode"`
	LastError string `json:"lastError"` // почему не запущено (пусто — всё хорошо)
}

// GetAutoHeal отдаёт состояние автолечения.
func (a *App) GetAutoHeal() AutoHealStatus {
	autoWatchMu.Lock()
	active := autoWatchActive
	lastErr := autoWatchErr
	autoWatchMu.Unlock()
	return AutoHealStatus{
		Enabled:   a.cfg.AutoHealEnabled,
		DryRun:    a.autoHealDryRunNow(),
		Active:    active,
		Mode:      a.autoHealModeLabel(),
		LastError: lastErr,
	}
}

// RestartAutoWatch — перезапустить наблюдение из UI (например, после ошибки
// запуска сниффера), не перезапуская всё приложение.
func (a *App) RestartAutoWatch() error {
	if !a.cfg.AutoHealEnabled {
		return errors.New("автолечение выключено — включите «следить за браузером»")
	}
	if !winutil.IsAdmin() {
		return errors.New("нужны права администратора")
	}
	if !a.zap.Installed() {
		return errors.New("zapret не установлен")
	}
	a.autoWatchPause()
	a.autoWatchResumeNow()
	if !a.isAutoWatchActive() {
		autoWatchMu.Lock()
		reason := autoWatchErr
		autoWatchMu.Unlock()
		if reason == "" {
			reason = "смотрите журнал"
		}
		return errors.New("наблюдение не запустилось: " + reason)
	}
	a.pushLog("[автолечение] наблюдение перезапущено вручную")
	return nil
}

// SetAutoHealEnabled включает/выключает наблюдение.
func (a *App) SetAutoHealEnabled(on bool) error {
	a.cfg.AutoHealEnabled = on
	if err := a.cfg.Save(); err != nil {
		return err
	}
	if on {
		a.autoWatchStart()
	} else {
		a.autoWatchStop()
	}
	return nil
}

// SetAutoHealDryRun переключает «только журнал» / «лечить автоматически».
func (a *App) SetAutoHealDryRun(dry bool) error {
	a.cfg.AutoHealDryRun = dry
	a.setAutoHealDryRun(dry)
	if err := a.cfg.Save(); err != nil {
		return err
	}
	a.pushLog("[автолечение] режим: " + a.autoHealModeLabel())
	return nil
}
