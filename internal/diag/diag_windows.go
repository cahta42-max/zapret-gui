//go:build windows

// Package diag — перенос диагностики из service.bat: BFE, TCP timestamps,
// системный прокси, конфликтующие службы, Secure DNS, hosts, WinDivert.
package diag

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
	"zapret_gui/internal/winutil"
)

// Run выполняет все проверки. toolsZapretBin — путь к bin\ дистрибутива
// zapret (для проверки WinDivert64.sys); может быть пустым.
func Run(zapretBinDir string) []Check {
	var checks []Check
	checks = append(checks, checkBFE(), checkTCPtimestamps(), checkProxy(),
		checkProcess("AdguardSvc.exe", "Adguard", "fail",
			"Adguard может мешать работе Discord", "https://github.com/Flowseal/zapret-discord-youtube/issues/417"),
		checkServices([]string{"Killer"}, "Killer-сервисы", "fail",
			"Killer конфликтует с zapret", "https://github.com/Flowseal/zapret-discord-youtube/issues/2512#issuecomment-2821119513"),
		checkServices([]string{"Intel", "Connectivity", "Network"}, "Intel Connectivity Network Service", "fail",
			"Конфликтует с zapret", "https://github.com/ValdikSS/GoodbyeDPI/issues/541#issuecomment-2661670982"),
		checkServices([]string{"TracSrvWrapper"}, "Check Point", "fail", "Конфликтует с zapret", ""),
		checkServices([]string{"EPWD"}, "Check Point EPWD", "fail", "Конфликтует с zapret", ""),
		checkServices([]string{"SmartByte"}, "SmartByte", "fail", "Отключите через services.msc", ""),
		checkServices([]string{"VPN"}, "VPN-сервисы", "warn", "VPN может конфликтовать с zapret", ""),
		checkDoH(), checkHosts(), checkWinDivert(zapretBinDir))
	return checks
}

func scQueryLines() string {
	out, err := winutil.HiddenCmd("sc", "query").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func checkBFE() Check {
	c := Check{Name: "Base Filtering Engine", Url: ""}
	out, err := winutil.HiddenCmd("sc", "query", "BFE").Output()
	if err == nil && strings.Contains(string(out), "RUNNING") {
		c.Status = "ok"
	} else {
		c.Status = "fail"
		c.Detail = "Служба BFE не запущена — zapret не будет работать"
	}
	return c
}

func checkTCPtimestamps() Check {
	c := Check{Name: "TCP timestamps"}
	out, err := winutil.HiddenCmd("netsh", "interface", "tcp", "show", "global").Output()
	if err != nil {
		c.Status, c.Detail = "warn", "Не удалось проверить"
		return c
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(strings.ToLower(line), "timestamps") {
			if strings.Contains(strings.ToLower(line), "enabled") {
				c.Status = "ok"
			} else {
				// авто-исправление (мы и так под админом)
				if err := winutil.HiddenCmd("netsh", "interface", "tcp", "set", "global",
					"timestamps=enabled").Run(); err == nil {
					c.Status, c.Detail = "ok", "Был отключён — включили автоматически"
				} else {
					c.Status, c.Detail = "warn", "Отключены; включить не удалось"
				}
			}
			return c
		}
	}
	c.Status, c.Detail = "warn", "Состояние не определено"
	return c
}

func checkProxy() Check {
	c := Check{Name: "Системный прокси"}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		enabled, _, err := k.GetIntegerValue("ProxyEnable")
		if err == nil && enabled == 1 {
			server, _, _ := k.GetStringValue("ProxyServer")
			c.Status = "warn"
			c.Detail = "Включён системный прокси: " + server + ". Убедитесь, что он корректен"
			return c
		}
	}
	c.Status = "ok"
	return c
}

func checkProcess(name, label, status, detail, url string) Check {
	c := Check{Name: label, Status: "ok", Url: url}
	out, _ := winutil.HiddenCmd("tasklist", "/FI", "IMAGENAME eq "+name).Output()
	if strings.Contains(strings.ToLower(string(out)), strings.ToLower(name)) {
		c.Status = status
		c.Detail = detail
	}
	return c
}

// checkServices ищет строки sc query, содержащие ВСЕ подстроки.
func checkServices(all []string, label, status, detail, url string) Check {
	c := Check{Name: label, Status: "ok", Url: url}
	if scContainsAll(scQueryLines(), all) {
		c.Status = status
		c.Detail = detail
	}
	return c
}

func scContainsAll(scOutput string, subs []string) bool {
	for _, line := range strings.Split(scOutput, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "service_name") {
			matched := true
			for _, s := range subs {
				if !strings.Contains(low, strings.ToLower(s)) {
					matched = false
					break
				}
			}
			if matched {
				return true
			}
		}
	}
	return false
}

var dohBase = `System\CurrentControlSet\Services\Dnscache\InterfaceSpecificParameters`

func checkDoH() Check {
	c := Check{Name: "Secure DNS (DoH)"}
	found := false
	if err := walkDoh(dohBase, &found); err == nil && found {
		c.Status = "ok"
		return c
	}
	c.Status = "warn"
	c.Detail = "Настройте Secure DNS в браузере (Chrome/Firefox) или в Windows 11 — иначе YouTube может не работать"
	return c
}

func walkDoh(path string, found *bool) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.READ)
	if err != nil {
		return err
	}
	defer k.Close()
	// сам ключ может содержать DohFlags (в подсубключах интерфейсов)
	names, err := k.ReadSubKeyNames(-1)
	if err == nil {
		for _, n := range names {
			sub, err := registry.OpenKey(registry.LOCAL_MACHINE, path+`\`+n, registry.READ)
			if err == nil {
				// DohFlags у протокольных подключей
				if v, _, err := sub.GetIntegerValue("DohFlags"); err == nil && v > 0 {
					*found = true
				}
				sub.Close()
			}
			if *found {
				return nil
			}
			_ = walkDoh(path+`\`+n, found)
			if *found {
				return nil
			}
		}
	}
	return nil
}

func checkHosts() Check {
	c := Check{Name: "Файл hosts"}
	data, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), `System32\drivers\etc\hosts`))
	if err != nil {
		c.Status = "warn"
		c.Detail = "Не удалось прочитать hosts"
		return c
	}
	low := strings.ToLower(string(data))
	if strings.Contains(low, "youtube.com") || strings.Contains(low, "youtu.be") {
		c.Status = "warn"
		c.Detail = "В hosts есть записи для youtube.com/youtu.be — это может мешать"
		return c
	}
	c.Status = "ok"
	return c
}

func checkWinDivert(binDir string) Check {
	c := Check{Name: "WinDivert64.sys"}
	if binDir == "" {
		c.Status, c.Detail = "warn", "Дистрибутив zapret не установлен"
		return c
	}
	matches, _ := filepath.Glob(filepath.Join(binDir, "*.sys"))
	if len(matches) == 0 {
		c.Status, c.Detail = "fail", "WinDivert64.sys не найден — вероятно, удалён антивирусом. Добавьте папку в исключения и обновите zapret"
		return c
	}
	// осиротевшая служба WinDivert при незапущенном winws
	out, _ := winutil.HiddenCmd("sc", "query", "WinDivert").Output()
	if strings.Contains(string(out), "RUNNING") {
		winwsOut, _ := winutil.HiddenCmd("tasklist", "/FI", "IMAGENAME eq winws.exe").Output()
		if !strings.Contains(strings.ToLower(string(winwsOut)), "winws.exe") {
			c.Status = "warn"
			c.Detail = "Служба WinDivert активна без winws — можно очистить кнопкой «Удалить службу»"
			return c
		}
	}
	c.Status = "ok"
	return c
}
