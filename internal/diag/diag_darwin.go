//go:build darwin

// Package diag — диагностика на macOS для движка utunws (ZapretMac):
// DNS (внешний обязателен!), hosts, VPN-туннели, состояние демона и
// utun50, keepinit, остатки SpoofDPI (системный прокси), списки.
package diag

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"zapret_gui/internal/utunws"
)

// Run выполняет все проверки macOS. ut — менеджер движка.
func Run(ut *utunws.Manager) []Check {
	var checks []Check
	checks = append(checks,
		checkDNS(),
		checkHosts(),
		checkVPN(),
		checkEngine(ut),
		checkDaemon(ut),
		checkKeepinit(),
		checkProxyLeftover(),
		checkLists(ut),
	)
	return checks
}

func out(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(b)
}

// checkDNS — активные резолверы (scutil --dns). utunws требует ВНЕШНИЙ
// DNS: провайдерский резолвер отравляется, и обход бессилен
// (docs/macos-engine-utunws.md).
func checkDNS() Check {
	c := Check{Name: "DNS-резолверы"}
	s := out("scutil", "--dns")
	if s == "" {
		c.Status, c.Detail = "warn", "Не удалось прочитать (scutil --dns)"
		return c
	}
	var servers []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver[") && len(servers) < 3 {
			f := strings.Fields(line)
			if len(f) == 2 {
				servers = append(servers, f[1])
			}
		}
	}
	if len(servers) == 0 {
		c.Status, c.Detail = "warn", "Резолверы не определены"
		return c
	}
	c.Status = "ok"
	c.Detail = strings.Join(servers, ", ")
	switch {
	case isLocalDNS(servers[0]):
		c.Status = "warn"
		c.Detail += " — локальный (VPN/AdGuard?): может мешать обходу"
	case !isPublicDNS(servers[0]):
		c.Status = "warn"
		c.Detail += " — похоже на DNS роутера/провайдера: YouTube может не открываться. Настройте внешний DNS (8.8.8.8 / 1.1.1.1) или DoH-профиль"
		c.Url = "https://github.com/paulmillr/encrypted-dns"
	}
	return c
}

// isLocalDNS — 127.x / ::1 (локальный резолвер).
func isLocalDNS(ip string) bool {
	return strings.HasPrefix(ip, "127.") || ip == "::1"
}

// isPublicDNS — известные публичные резолверы.
func isPublicDNS(ip string) bool {
	public := map[string]bool{
		"8.8.8.8": true, "8.8.4.4": true, // Google
		"1.1.1.1": true, "1.0.0.1": true, // Cloudflare
		"9.9.9.9": true, "149.112.112.112": true, // Quad9
		"77.88.8.8": true, "77.88.8.1": true, // Yandex
		"94.140.14.14": true, "94.140.15.15": true, // AdGuard
		"208.67.222.222": true, "208.67.220.220": true, // Cisco
		"2001:4860:4860::8888": true, "2001:4860:4860::8844": true,
		"2606:4700:4700::1111": true, "2606:4700:4700::1001": true,
	}
	return public[ip]
}

// checkHosts — записи youtube в /etc/hosts.
func checkHosts() Check {
	c := Check{Name: "Файл hosts"}
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		c.Status, c.Detail = "warn", "Не удалось прочитать /etc/hosts"
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

// checkVPN — utun-интерфейсы КРОМЕ нашего utun50: активный VPN-туннель
// всего трафика перехватывает его до pf.
func checkVPN() Check {
	c := Check{Name: "Виртуальные интерфейсы (VPN)"}
	s := out("ifconfig")
	n := strings.Count(s, "utun")
	ours := 0
	if strings.Contains(s, "utun50") {
		ours = 1
	}
	n -= ours
	switch {
	case n == 0:
		c.Status, c.Detail = "ok", "лишних VPN-интерфейсов нет"
	case n <= 3:
		// 1-3 utun — норма для самой macOS (iCloud Private Relay и т.п.)
		c.Status = "ok"
		c.Detail = itoa(n) + " utun — штатно для macOS"
	default:
		c.Status = "warn"
		c.Detail = itoa(n) + " utun-интерфейсов — вероятно, активен VPN; он может конфликтовать с обходом"
	}
	return c
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// checkEngine — установлен ли движок utunws и его версия.
func checkEngine(ut *utunws.Manager) Check {
	c := Check{Name: "Движок utunws"}
	if !ut.Installed() {
		c.Status, c.Detail = "warn", "Движок не установлен"
		return c
	}
	detail := "установлен"
	if v := ut.Version(); v != "" {
		detail = "версия " + v
	}
	if ut.Running() {
		if s, ok := ut.StrategyByID(ut.SelectedStrategy()); ok {
			detail += ", стратегия " + s.Title
		}
		c.Status, c.Detail = "ok", detail
	} else {
		c.Status = "warn"
		c.Detail = detail + " — не запущен"
	}
	return c
}

// checkDaemon — состояние LaunchDaemon движка.
func checkDaemon(ut *utunws.Manager) Check {
	c := Check{Name: "Служба (LaunchDaemon)"}
	if !ut.Installed() {
		c.Status, c.Detail = "ok", "Не применимо"
		return c
	}
	state := ut.DaemonState()
	if strings.Contains(state, "running") {
		c.Status, c.Detail = "ok", state
	} else if ut.Running() {
		c.Status = "ok"
		c.Detail = state + " (туннель поднят)"
	} else {
		c.Status = "warn"
		c.Detail = state
	}
	return c
}

// checkKeepinit — TCP-тюнинг движка (7000 при запущенном utunws).
func checkKeepinit() Check {
	c := Check{Name: "net.inet.tcp.keepinit"}
	v := strings.TrimSpace(out("sysctl", "-n", "net.inet.tcp.keepinit"))
	if v == "" {
		c.Status, c.Detail = "warn", "Не удалось прочитать"
		return c
	}
	if v == "7000" {
		c.Status, c.Detail = "ok", "7000 (тюнинг движка активен)"
		return c
	}
	c.Status = "ok"
	c.Detail = v + " (штатный; движок выставит 7000 при запуске)"
	return c
}

// checkProxyLeftover — включённый системный прокси (остаток SpoofDPI
// или сторонний) ломает перехват utunws.
func checkProxyLeftover() Check {
	c := Check{Name: "Системный прокси"}
	s := out("scutil", "--proxy")
	enabled := strings.Contains(s, "HTTPEnable : 1") ||
		strings.Contains(s, "HTTPSEnable : 1") ||
		strings.Contains(s, "ProxyAutoConfigEnable : 1")
	if enabled {
		c.Status = "warn"
		c.Detail = "Включён — при работе utunws должен быть ВЫКЛЮЧЕН (остаток spoofdpi?)"
		return c
	}
	c.Status, c.Detail = "ok", "Выключен"
	return c
}

// checkLists — пользовательские списки в DataRoot (run.sh требует все 8).
func checkLists(ut *utunws.Manager) Check {
	c := Check{Name: "Списки хостов"}
	if !ut.Installed() {
		c.Status, c.Detail = "ok", "Не применимо"
		return c
	}
	required := []string{
		"list-general.txt", "list-general-user.txt", "list-google.txt",
		"list-exclude.txt", "list-exclude-user.txt",
		"ipset-all.txt", "ipset-exclude.txt", "ipset-exclude-user.txt",
	}
	missing := 0
	for _, name := range required {
		st, err := os.Stat(filepath.Join(ut.ListsDir(), name))
		if err != nil || st.IsDir() {
			missing++
		}
	}
	if missing > 0 {
		c.Status = "fail"
		c.Detail = "Не хватает файлов: " + itoa(missing) + " (переустановите движок)"
		return c
	}
	c.Status, c.Detail = "ok", "На месте ("+ut.ListsDir()+")"
	return c
}
