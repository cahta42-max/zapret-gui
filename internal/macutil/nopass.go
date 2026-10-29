//go:build darwin

package macutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Однократный пароль: sudoers.d + root-обёртки.
//
// Каждый Старт/Стоп движка раньше просил пароль (osascript). Теперь при
// установке движка (или первым Стартом на уже установленном) тем же
// единственным osascript ставятся:
//   /Library/PrivilegedHelperTools/com.buzmakov.ZapretGui/engine-restart
//   /Library/PrivilegedHelperTools/com.buzmakov.ZapretGui/engine-stop
//   /Library/PrivilegedHelperTools/com.buzmakov.ZapretGui/probe
// и /etc/sudoers.d/zapretgui — NOPASSWD ТОЛЬКО на эти три файла, без
// аргументов и wildcards. Обёртки — фиксированные скрипты root:wheel 0755
// в пути без пробелов (в sudoers пробелы в пути не выживают); реальные
// скрипты движка лежат в root-каталоге и юзером не редактируются, так
// что root-эскалация через них невозможна.
//
// Sudoers-файл валидируется visudo -cf ДО переезда в /etc/sudoers.d:
// битый файл там ломает sudo целиком — этого не допускаем. Скрипт
// настройки пишется в /tmp и запускается одной строкой (osascript
// не принимает многострочные команды — тот же паттерн, что в testloop).

// NopassDir — каталог root-обёрток (путь БЕЗ пробелов — важно для sudoers).
const NopassDir = "/Library/PrivilegedHelperTools/com.buzmakov.ZapretGui"

// nopassSetupScript — sh-скрипт, исполняемый от root (аргумент — юзер).
// Создаёт обёртки и sudoers; при ошибке валидации НЕ трогает /etc/sudoers.d.
const nopassSetupScript = `#!/bin/sh
set -e
USER_NAME="$1"
DIR='` + NopassDir + `'
ENGINE='/Library/Application Support/ZapretMac'
SUDOERS=/etc/sudoers.d/zapretgui

mkdir -p "$DIR"
printf '%s\n' '#!/bin/sh' "exec /bin/sh '$ENGINE/restart.sh'" > "$DIR/engine-restart"
printf '%s\n' '#!/bin/sh' "exec /bin/sh '$ENGINE/stop.sh'"     > "$DIR/engine-stop"
printf '%s\n' '#!/bin/sh' 'echo ok'                             > "$DIR/probe"
chown -R root:wheel "$DIR"
chmod 755 "$DIR" "$DIR/engine-restart" "$DIR/engine-stop" "$DIR/probe"

printf '%s ALL=(root) NOPASSWD: %s/engine-restart, %s/engine-stop, %s/probe\n' \
  "$USER_NAME" "$DIR" "$DIR" "$DIR" > /tmp/zapretgui.sudoers.new
/usr/sbin/visudo -cf /tmp/zapretgui.sudoers.new
cp /tmp/zapretgui.sudoers.new "$SUDOERS"
chown root:wheel "$SUDOERS"
chmod 440 "$SUDOERS"
rm -f /tmp/zapretgui.sudoers.new
echo 'nopass: ok'
`

// InstallPasswordless ставит обёртки+sudoers одним osascript-паролем.
// Промпт объясняет юзеру, что это последний раз. При отказе от пароля
// или ошибке валидации sudoers возвращает ошибку (вызывающий просто
// продолжает работать по-старому, с паролями).
func InstallPasswordless(prompt string) error {
	user := os.Getenv("USER")
	if user == "" {
		out, err := exec.Command("id", "-un").Output()
		if err != nil {
			return fmt.Errorf("не определить пользователя: %v", err)
		}
		user = strings.TrimSpace(string(out))
	}
	scriptPath := "/tmp/zapretgui-nopass-setup.sh"
	if err := os.WriteFile(scriptPath, []byte(nopassSetupScript), 0o755); err != nil {
		return fmt.Errorf("запись скрипта настройки: %v", err)
	}
	defer os.Remove(scriptPath)
	shell := fmt.Sprintf("sh %s %s", shqApple(scriptPath), shqApple(user))
	if err := RunAdminSync(prompt, shell); err != nil {
		return err
	}
	InvalidateNopassCache()
	return nil
}

// shqApple — одинарное экранирование пути для sh-команды в osascript.
func shqApple(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var (
	nopassChecked bool
	nopassCached  bool
)

// PasswordlessAvailable — молча (sudo -n, без пароля) проверяет, что
// sudoers работает. Результат кэшируется на процесс.
func PasswordlessAvailable() bool {
	if nopassChecked {
		return nopassCached
	}
	err := exec.Command("sudo", "-n", NopassDir+"/probe").Run()
	nopassCached = err == nil
	nopassChecked = true
	return nopassCached
}

// RunPasswordless выполняет обёртку из NopassDir через sudo -n:
// пароль не запрашивается; при недоступности sudoers — ошибка,
// вызывающий откатывается на osascript.
func RunPasswordless(name string) error {
	out, err := exec.Command("sudo", "-n", NopassDir+"/"+name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("sudo %s: %s", name, msg)
	}
	return nil
}

// InvalidateNopassCache сбрасывает кэш проверки (после установки
// sudoers из этого же процесса).
func InvalidateNopassCache() {
	nopassChecked = false
	nopassCached = false
}
