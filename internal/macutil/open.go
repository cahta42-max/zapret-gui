//go:build darwin

package macutil

import (
	"os"
	"os/exec"
)

// OpenFolder открывает папку в Finder.
func OpenFolder(path string) { Silent("open", path) }

// OpenURL открывает ссылку/файл системным обработчиком (tg:// — Telegram).
func OpenURL(url string) { Silent("open", url) }

// RunAsRootDetached запускает команду от root через osascript (один запрос
// пароля) в фоне — для движка с raw-сокетами (fake-пакеты SpoofDPI).
// Возвращаемая ошибка относится к osascript, не к самому процессу.
func RunAsRootDetached(exe string, args ...string) error {
	shell := fmtShellCommand(exe, args...)
	script := `do shell script "` + escapeAppleScript(shell) + ` > /dev/null 2>&1 &" with administrator privileges with prompt "Zapret GUI: запуск движка обхода"`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return &RootError{err, string(out)}
	}
	return nil
}

// RootError — отказ/ошибка элевации (пользователь нажал «Отмена» и т.п.).
type RootError struct {
	Err    error
	Output string
}

func (e *RootError) Error() string {
	if e.Err != nil && len(e.Output) == 0 {
		return e.Err.Error()
	}
	return e.Output
}

// IsRoot — запущен ли процесс от root.
func IsRoot() bool { return os.Geteuid() == 0 }

func fmtShellCommand(exe string, args ...string) string {
	cmd := quoteShell(exe)
	for _, a := range args {
		cmd += " " + quoteShell(a)
	}
	return cmd
}

func quoteShell(s string) string {
	return "'" + replaceAll(s, "'", `'\''`) + "'"
}

func replaceAll(s, old, new string) string {
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}

// escapeAppleScript — экранирование для AppleScript-строки.
func escapeAppleScript(s string) string {
	r := replaceAll(s, `\`, `\\`)
	r = replaceAll(r, `"`, `\"`)
	return r
}
