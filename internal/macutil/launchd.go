//go:build darwin

package macutil

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Автозапуск GUI на macOS — LaunchAgent пользователя (без root):
// ~/Library/LaunchAgents/<label>.plist, стартует при входе в систему.

func agentPlistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// SetAutostart включает/выключает автозапуск приложения (LaunchAgent).
// exe запускается с ключом -minimized (в трей).
func SetAutostart(label string, exe string, enable bool) error {
	path, err := agentPlistPath(label)
	if err != nil {
		return err
	}
	if !enable {
		Silent("launchctl", "unload", path)
		if _, err := os.Stat(path); err == nil {
			return os.Remove(path)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>-minimized</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/zapretgui-launchd.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/zapretgui-launchd.log</string>
</dict>
</plist>
`, label, exe)
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	Silent("launchctl", "unload", path) // перезагрузка конфигурации
	Silent("launchctl", "load", path)
	return nil
}

// AutostartEnabled — установлен ли LaunchAgent.
func AutostartEnabled(label string) bool {
	path, err := agentPlistPath(label)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// stampPath — файл-метка времени последнего запуска (диагностика).
func stampPath() string {
	dir, _ := os.UserCacheDir()
	return filepath.Join(dir, "ZapretGui", "last-run.txt")
}

// WriteStartupStamp — время старта (для справки в диагностике).
func WriteStartupStamp() {
	_ = os.MkdirAll(filepath.Dir(stampPath()), 0o755)
	_ = os.WriteFile(stampPath(), []byte(time.Now().Format(time.RFC3339)), 0o644)
}
