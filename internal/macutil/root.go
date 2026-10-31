//go:build darwin

package macutil

import (
	"fmt"
	"os/exec"
	"strings"
)

// RunAdminSync выполняет shell-команду от root (osascript, один запрос
// пароля) и ДОЖИДАЕТСЯ завершения — в отличие от RunAsRootDetached.
// shell — уже готовая sh-команда (перенаправления вывода — на совести
// вызывающего). Ошибка возвращается и при отказе от пароля, и при
// ненулевом коде выхода самой команды (osascript пишет «error: ... exit
// status N»).
func RunAdminSync(prompt, shell string) error {
	script := `do shell script "` + escapeAppleScript(shell) +
		`" with administrator privileges with prompt "` + prompt + `"`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(strings.ToLower(msg), "user canceled") ||
			strings.Contains(strings.ToLower(msg), "user cancelled") ||
			strings.Contains(msg, "-128") {
			return fmt.Errorf("запрос прав администратора отменён")
		}
		return fmt.Errorf("sudo: %s", msg)
	}
	return nil
}

// ForceProxyOff безусловно выключает HTTP/HTTPS-прокси (миграция со
// SpoofDPI: состояние могло остаться с прошлой сессии). Пробуем scutil,
// затем networksetup по всем сервисам (v1 мог поставить персистентно —
// на новых macOS запись scutil State: запрещена, а networksetup
// -set*proxystate работает от обычного пользователя).
func (pm *ProxyManager) ForceProxyOff() error {
	pm.mu.Lock()
	pm.enabled = false
	pm.mu.Unlock()
	// scutil-попытка (не критична, если запрещено)
	script := `open
get State:/Network/Global/Proxies
d.add HTTPEnable 0
d.add HTTPProxy 0.0.0.0
d.add HTTPPort 0
d.add HTTPSEnable 0
d.add HTTPSProxy 0.0.0.0
d.add HTTPSPort 0
set State:/Network/Global/Proxies
quit
`
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	_, _ = cmd.CombinedOutput()

	// networksetup: выключаем везде (персистентная настройка v1)
	for _, svc := range listNetworkServices() {
		Silent("networksetup", "-setwebproxystate", svc, "off")
		Silent("networksetup", "-setsecurewebproxystate", svc, "off")
	}

	// проверка
	st, err := ReadProxy()
	if err != nil {
		return err
	}
	if st.HTTPEnable == 1 || st.HTTPSEnable == 1 {
		return fmt.Errorf("прокси всё ещё включён (networksetup не помог)")
	}
	return nil
}
