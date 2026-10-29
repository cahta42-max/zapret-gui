//go:build darwin

package main

import "errors"

// Автолечение (наблюдение за браузером через WinDivert) — только Windows:
// на macOS свой сетевой движок (utunws), там сниффер не реализуем тем же
// способом. Заглушки держат биндинги одинаковыми для обеих платформ.

const autoHealFlowTimeout = 0

type AutoHealStatus struct {
	Enabled   bool   `json:"enabled"`
	DryRun    bool   `json:"dryRun"`
	Active    bool   `json:"active"`
	Mode      string `json:"mode"`
	LastError string `json:"lastError"`
}

func (a *App) startAutoWatch() {}
func (a *App) autoWatchStart() {}
func (a *App) autoWatchStop()  {}

func (a *App) autoHealModeLabel() string { return "недоступно на macOS" }

func (a *App) GetAutoHeal() AutoHealStatus {
	return AutoHealStatus{Enabled: false, DryRun: true, Active: false, Mode: "недоступно на macOS",
		LastError: "автолечение доступно только в Windows-версии"}
}

func (a *App) RestartAutoWatch() error {
	return errors.New("автолечение доступно только в Windows-версии")
}

func (a *App) SetAutoHealEnabled(bool) error {
	return errors.New("автолечение доступно только в Windows-версии")
}

func (a *App) SetAutoHealDryRun(bool) error {
	return errors.New("автолечение доступно только в Windows-версии")
}
