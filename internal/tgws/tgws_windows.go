//go:build windows

package tgws

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"zapret_gui/internal/release"
	"zapret_gui/internal/winutil"
)

func (m *Manager) exePath() string { return filepath.Join(m.dir, "TgWsProxy.exe") }

// fetchAndInstall — один exe из релиза.
func (m *Manager) fetchAndInstall(ctx context.Context, progress func(pct float64, stage string)) (string, error) {
	tag, asset, err := release.LatestTgws(ctx)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", err
	}
	if err := release.Download(ctx, asset.URL, m.exePath(), func(sent, total int64) {
		if progress != nil && total > 0 {
			progress(float64(sent)/float64(total), "Скачиваю TgWsProxy…")
		}
	}); err != nil {
		return "", err
	}
	return tag, nil
}

func (m *Manager) versionFromBinary() string { return winutil.ExeFileVersion(m.exePath()) }

// processNames — имена процессов прокси: наш и ранее распространявшийся
// TgWsProxy_windows.exe (старые автозапуски).
var processNames = []string{"TgWsProxy.exe", "TgWsProxy_windows.exe"}

func (m *Manager) killProxyProcesses() {
	for _, name := range processNames {
		_ = winutil.HiddenCmd("taskkill", "/IM", name, "/F").Run()
	}
}

func (m *Manager) anyProxyRunning() bool {
	for _, name := range processNames {
		if isProcessRunning(name) {
			return true
		}
	}
	return false
}

func isProcessRunning(name string) bool {
	out, err := winutil.HiddenCmd("tasklist", "/FI", "IMAGENAME eq "+name).Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(name))
}

// legacyConfigPath — %APPDATA%\TgWsProxy\config.json (установленная ранее
// версия прокси).
func (m *Manager) legacyConfigPath() string {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return ""
	}
	legacy := filepath.Join(appdata, "TgWsProxy", "config.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return ""
}
