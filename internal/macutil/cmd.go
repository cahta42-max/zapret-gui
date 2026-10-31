//go:build darwin

// Package macutil — обёртки над macOS: системный прокси (scutil/networksetup),
// LaunchAgent-автозапуск, единственный экземпляр (flock), запуск с правами
// администратора (osascript), открытие папок/ссылок.
package macutil

import "os/exec"

// Cmd — обычная команда (на macOS консольных окон у дочерних процессов нет).
func Cmd(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

// Output выполняет команду и возвращает stdout (ошибка пробрасывается).
func Output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// Silent выполняет команду, игнорируя вывод и ошибки (best-effort).
func Silent(name string, args ...string) {
	_ = exec.Command(name, args...).Run()
}
