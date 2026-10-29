//go:build windows

package main

import (
	"strings"
	"sync"
	"time"

	"zapret_gui/internal/vpndetect"
)

// vpnS — состояние автопаузы zapret при VPN.
var vpnS = struct {
	mu         sync.Mutex
	info       vpndetect.Info
	lastActive bool   // предыдущее значение Active (для перехода)
	adapterKey string // подпись списка адаптеров (чтобы не спамить журнал)
	pausedByUs bool   // мы останавливали zapret из-за VPN
}{}

// startVpnWatch — фоновый опрос состояния VPN (раз в 3 с).
func (a *App) startVpnWatch() {
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			a.vpnTick()
			<-t.C
		}
	}()
}

// vpnTick — один цикл: определить VPN и, если состояние поменялось, отреагировать.
func (a *App) vpnTick() {
	info := vpndetect.Detect()
	vpnS.mu.Lock()
	prev := vpnS.lastActive
	vpnS.info = info
	vpnS.lastActive = info.Active
	adapterKey := strings.Join(info.Adapters, ",")
	adapterChanged := adapterKey != vpnS.adapterKey
	vpnS.adapterKey = adapterKey
	vpnS.mu.Unlock()

	// туннель поднят, но интернет через него не идёт (Tailscale/Clash в режиме
	// mesh): zapret не паузим, но один раз объясняем в журнале
	logAdapterOnly := adapterChanged && len(info.Adapters) > 0 && !info.Active
	if !a.cfg.PauseOnVpn {
		return
	}
	switch {
	case !prev && info.Active:
		a.vpnEnter(info)
	case prev && !info.Active:
		a.vpnLeave(info)
	case logAdapterOnly:
		line := "[vpn] поднят туннель " + strings.Join(info.Adapters, ", ") +
			" — интернет идёт не через него, zapret не паузим"
		a.pushLog(line)
		bootLog("vpn: %s", line)
	}
}

// vpnEnter — интернет пошёл через VPN: убираем обход, наблюдение на паузу.
func (a *App) vpnEnter(info vpndetect.Info) {
	name := info.Name
	if name == "" {
		name = "VPN"
	}
	running := a.zapRunning()
	vpnS.mu.Lock()
	vpnS.pausedByUs = running
	vpnS.mu.Unlock()
	if a.cfg.AutoHealEnabled {
		a.autoWatchPause()
	}
	if running {
		if err := a.stopZapretInternal(); err != nil {
			a.pushLog("[vpn] " + name + ": не удалось остановить zapret: " + err.Error())
		} else {
			a.pushLog("[vpn] " + name + ": интернет через VPN — zapret остановлен, верну после выключения VPN")
			bootLog("vpn: %s: активен, zapret остановлен", name)
		}
	} else {
		a.pushLog("[vpn] " + name + ": интернет через VPN — zapret не запущен")
	}
	a.emitState()
}

// vpnLeave — VPN выключили: возвращаем обход и наблюдение.
func (a *App) vpnLeave(info vpndetect.Info) {
	vpnS.mu.Lock()
	paused := vpnS.pausedByUs
	vpnS.pausedByUs = false
	vpnS.mu.Unlock()
	if paused {
		if err := a.zap.EnsureRunning(a.cfg.LastStrategy); err != nil {
			a.pushLog("[vpn] VPN выключен, но zapret не поднялся: " + err.Error())
		} else {
			a.pushLog("[vpn] VPN выключен — zapret возвращён")
			bootLog("vpn: выключен, zapret возвращён")
		}
	} else {
		a.pushLog("[vpn] VPN выключен — zapret не трогаю (его не останавливали)")
	}
	a.emitState()
	if a.cfg.AutoHealEnabled {
		a.autoWatchResume(2 * time.Second)
	}
}

// vpnInfo — текущее состояние VPN (для статуса в UI).
func (a *App) vpnInfo() vpndetect.Info {
	vpnS.mu.Lock()
	defer vpnS.mu.Unlock()
	return vpnS.info
}

// vpnStartupHold — VPN уже несёт интернет при старте приложения: zapret не
// поднимаем и наблюдение не запускаем, чтобы не мешать VPN.
func (a *App) vpnStartupHold() bool {
	if !a.cfg.PauseOnVpn {
		return false
	}
	info := vpndetect.Detect()
	vpnS.mu.Lock()
	vpnS.info = info
	vpnS.lastActive = info.Active
	vpnS.adapterKey = strings.Join(info.Adapters, ",")
	vpnS.mu.Unlock()
	if !info.Active {
		if len(info.Adapters) > 0 {
			line := "[vpn] поднят туннель " + strings.Join(info.Adapters, ", ") +
				" — интернет идёт не через него, zapret не паузим"
			a.pushLog(line)
			bootLog("vpn: %s", line)
		}
		return false
	}
	name := info.Name
	if name == "" {
		name = "VPN"
	}
	// vpnHold — это startup(), службу могла поднять сама Windows до входа
	running := a.zapRunning()
	if a.cfg.AutoHealEnabled {
		a.autoWatchPause()
	}
	if running {
		if err := a.stopZapretInternal(); err != nil {
			a.pushLog("[vpn] " + name + ": не удалось остановить zapret: " + err.Error())
		} else {
			a.pushLog("[vpn] " + name + ": интернет через VPN — zapret остановлен, автолечение на паузе")
			bootLog("vpn: %s: активен (startup), zapret остановлен", name)
		}
	} else {
		a.pushLog("[vpn] " + name + ": интернет через VPN — zapret не запущен, автолечение на паузе")
	}
	// при выключении VPN вернём обход, если он должен был работать
	vpnS.mu.Lock()
	vpnS.pausedByUs = running || (a.cfg.ZapretAutoRun && a.cfg.StrategyChosen)
	vpnS.mu.Unlock()
	return true
}

// vpnManualStartWarning — пользователь поднимает zapret вручную при активном
// VPN: это его решение, автопауза не будет циклично гасить обход.
func (a *App) vpnManualStartWarning() {
	if !a.cfg.PauseOnVpn {
		return
	}
	info := a.vpnInfo()
	if !info.Active {
		return
	}
	name := info.Name
	if name == "" {
		name = "VPN"
	}
	a.pushLog("[vpn] " + name + " активен: запускаю zapret вручную — пока состояние VPN не сменится, автопауза не сработает")
}

// SetVpnPause включает/выключает автопаузу zapret при активном VPN.
func (a *App) SetVpnPause(on bool) error {
	a.cfg.PauseOnVpn = on
	if err := a.cfg.Save(); err != nil {
		return err
	}
	if on {
		a.pushLog("[vpn] автопауза при VPN включена")
	} else {
		a.pushLog("[vpn] автопауза при VPN выключена")
	}
	// выключили при активном VPN — сразу возвращаем zapret и наблюдение
	info := a.vpnInfo()
	if !on && info.Active {
		vpnS.mu.Lock()
		paused := vpnS.pausedByUs
		vpnS.pausedByUs = false
		vpnS.mu.Unlock()
		if paused {
			if err := a.zap.EnsureRunning(a.cfg.LastStrategy); err != nil {
				a.pushLog("[vpn] zapret не поднялся: " + err.Error())
			} else {
				a.pushLog("[vpn] автопауза выключена — zapret возвращён")
			}
		}
		if a.cfg.AutoHealEnabled {
			a.autoWatchResume(2 * time.Second)
		}
	}
	a.emitState()
	return nil
}
