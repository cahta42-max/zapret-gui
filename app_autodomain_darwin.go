//go:build darwin

package main

import (
	"errors"

	"zapret_gui/internal/domaincheck"
)

// CheckAndAddDomain — заглушка для macOS: автопроверка доменов сделана в
// Windows-версии (кнопка на macOS скрыта классом win-only). Метод должен
// существовать, иначе darwin-сборка не сгенерирует фронтенду биндинг.
func (a *App) CheckAndAddDomain(raw string) (domaincheck.Result, error) {
	return domaincheck.Result{}, errors.New("автопроверка доменов есть только в Windows-версии")
}
