//go:build darwin

package tgws

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"zapret_gui/internal/release"
)

// На macOS официальный дистрибутив — TgWsProxy_macos_universal.dmg с
// «TG WS Proxy.app» (universal2: Intel + Apple Silicon). Бинарь внутри:
// TG WS Proxy.app/Contents/MacOS/TgWsProxy (имя EXE из packaging/macos.spec).

const appBundleName = "TG WS Proxy.app"
const bundleBinary = "TgWsProxy"

func (m *Manager) appPath() string { return filepath.Join(m.dir, appBundleName) }

func (m *Manager) exePath() string {
	return filepath.Join(m.appPath(), "Contents", "MacOS", bundleBinary)
}

// fetchAndInstall: dmg → монтируем → копируем .app → размонтируем.
// Портативные данные (TgWsProxy_data внутри .app) переносим в новый бандл.
func (m *Manager) fetchAndInstall(ctx context.Context, progress func(pct float64, stage string)) (string, error) {
	tag, asset, err := release.LatestTgwsMac(ctx)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", err
	}
	tmpDmg := filepath.Join(os.TempDir(), "zapretgui-tgws.dmg")
	if err := release.Download(ctx, asset.URL, tmpDmg, func(sent, total int64) {
		if progress != nil && total > 0 {
			progress(float64(sent)/float64(total)*0.8, "Скачиваю TgWsProxy…")
		}
	}); err != nil {
		return "", err
	}
	defer os.Remove(tmpDmg)
	if progress != nil {
		progress(0.85, "Монтирую образ…")
	}

	// старые портативные данные — наружу, чтобы пережили замену .app
	dataBackup := ""
	oldData := filepath.Join(filepath.Dir(m.exePath()), "TgWsProxy_data")
	if st, err := os.Stat(oldData); err == nil && st.IsDir() {
		dataBackup = m.dir + ".data.bak"
		os.RemoveAll(dataBackup)
		if err := os.Rename(oldData, dataBackup); err != nil {
			dataBackup = ""
		}
	}
	os.RemoveAll(m.appPath())

	// hdiutil attach -nobrowse: без открытия Finder
	mnt := filepath.Join(os.TempDir(), "zapretgui-tgws-mnt")
	os.RemoveAll(mnt)
	if out, err := exec.Command("hdiutil", "attach", "-readonly", "-nobrowse",
		"-mountpoint", mnt, tmpDmg).CombinedOutput(); err != nil {
		return "", fmt.Errorf("hdiutil attach: %v: %s", err, strings.TrimSpace(string(out)))
	}
	defer exec.Command("hdiutil", "detach", mnt, "-force").Run()

	src := filepath.Join(mnt, appBundleName)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("в образе нет %s", appBundleName)
	}
	if progress != nil {
		progress(0.92, "Копирую приложение…")
	}
	if out, err := exec.Command("cp", "-R", src, m.appPath()).CombinedOutput(); err != nil {
		return "", fmt.Errorf("cp: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// возвращаем портативные данные в новый бандл
	if dataBackup != "" {
		os.RemoveAll(oldData)
		if err := os.Rename(dataBackup, oldData); err != nil {
			_ = err // не фатально: создастся дефолтный конфиг
		}
	}
	if err := os.Chmod(m.exePath(), 0o755); err != nil {
		return "", err
	}
	return tag, nil
}

// versionFromBinary — CFBundleShortVersionString из Info.plist.
func (m *Manager) versionFromBinary() string {
	data, err := os.ReadFile(filepath.Join(m.appPath(), "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	s := string(data)
	i := strings.Index(s, "CFBundleShortVersionString")
	if i < 0 {
		return ""
	}
	rest := s[i:]
	j := strings.Index(rest, "<string>")
	if j < 0 {
		return ""
	}
	rest = rest[j+len("<string>"):]
	k := strings.Index(rest, "</string>")
	if k < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:k])
}

// killProxyProcesses — pkill по пути к бинарю внутри .app (не убьёт сам GUI:
// наш процесс — ZapretGui, паттерн специфичен для TgWsProxy).
func (m *Manager) killProxyProcesses() {
	_ = exec.Command("pkill", "-f", appBundleName+"/Contents/MacOS/"+bundleBinary).Run()
}

func (m *Manager) anyProxyRunning() bool {
	out, err := exec.Command("pgrep", "-f",
		appBundleName+"/Contents/MacOS/"+bundleBinary).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// legacyConfigPath — ~/Library/Application Support/TgWsProxy/config.json
// (стандартный каталог приложения на macOS; куда он пишет сам).
func (m *Manager) legacyConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	legacy := filepath.Join(home, "Library", "Application Support", "TgWsProxy", "config.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return ""
}
