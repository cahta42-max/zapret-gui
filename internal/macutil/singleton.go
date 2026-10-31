//go:build darwin

package macutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// AcquireSingleInstance — единый экземпляр приложения через flock на файле
// в ~/Library/Caches (аналог именованного мьютекса на Windows).
// Возвращает release-функцию; false — приложение уже запущено.
func AcquireSingleInstance(name string) (func(), bool) {
	dir, err := os.UserCacheDir() // ~/Library/Caches
	if err != nil {
		return func() {}, true // не блокируем запуск
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}, true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, true
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false
	}
	return func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, true
}

// ActivateExistingApp выводит уже запущенное окно приложения на перед.
// С таймаутом: если целевое приложение зависло, osascript может висеть
// минутами (AppleEvents) — второй экземпляр зависнет вместе с ним.
func ActivateExistingApp(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "osascript", "-e",
		fmt.Sprintf(`tell application "%s" to activate`, name)).Run()
}
