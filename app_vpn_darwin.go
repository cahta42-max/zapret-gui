//go:build darwin

package main

import (
	"errors"

	"zapret_gui/internal/vpndetect"
)

// startVpnWatch — на macOS автопауза при VPN не реализована (сетевой движок
// utunws; VPN и обход там устроены иначе).
func (a *App) startVpnWatch() {}

// vpnInfo — на macOS VPN не определяется.
func (a *App) vpnInfo() vpndetect.Info { return vpndetect.Info{} }

// vpnStartupHold — на macOS не удерживаем запуск.
func (a *App) vpnStartupHold() bool { return false }

// vpnManualStartWarning — на macOS нечего предупреждать.
func (a *App) vpnManualStartWarning() {}

// SetVpnPause — на macOS переключатель недоступен.
func (a *App) SetVpnPause(bool) error {
	return errors.New("автопауза при VPN доступна только в Windows-версии")
}
