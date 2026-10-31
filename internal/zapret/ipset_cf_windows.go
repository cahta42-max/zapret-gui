//go:build windows

package zapret

import (
	"os"
	"path/filepath"
	"strings"

	"zapret_gui/internal/winutil"
)

// ipsetCFExcludeName — файл с диапазонами Cloudflare, который добавляется
// ТОЛЬКО в ipset-правила winws (профили с --ipset=...).
//
// Зачем: для обхода игровых серверов (FaceIT/CS2) люди включают IPSet «any»
// (пустой ipset-all.txt) и Game Filter — тогда ipset-правила применяются ко
// всем IP. Но через Cloudflare ходит tg-ws-proxy (WS-эндпоинты kws*.co.uk), и
// desync рвёт его TLS — Telegram перестаёт работать. Исключение Cloudflare из
// ipset-правил чинит tg-ws, при этом hostlist-правила (Discord и др.) остаются
// нетронутыми: их домены по-прежнему маскируются, иначе Discord не открывается
// (hostlist-профили тоже содержат --ipset-exclude, поэтому Cloudflare нельзя
// класть в общий ipset-exclude-user.txt — оттуда он выключил бы и hostlist).
const ipsetCFExcludeName = "ipset-exclude-cf.txt"

// cloudflareRanges — официальные диапазоны Cloudflare (cloudflare.com/ips),
// IPv4 + IPv6.
var cloudflareRanges = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

// SetTgwsCompat задаёт признак «tg-ws-proxy установлен». Пока он включён,
// в аргументы ipset-правил добавляется исключение Cloudflare. Хук нужен,
// чтобы пакет zapret не зависел от пакета tgws.
func (m *Manager) SetTgwsCompat(f func() bool) { m.tgwsCompat = f }

func (m *Manager) tgwsCompatOn() bool { return m.tgwsCompat != nil && m.tgwsCompat() }

// EnsureIpsetCFExclude создаёт/обновляет lists/ipset-exclude-cf.txt и
// возвращает путь к нему. Файл перезаписывается только при отличии содержимого.
func (m *Manager) EnsureIpsetCFExclude() string {
	path := filepath.Join(m.dir, "lists", ipsetCFExcludeName)
	want := strings.Join(cloudflareRanges, "\r\n") + "\r\n"
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return path
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(want), 0o644)
	return path
}

// injectIpsetCFExclude добавляет token в каждый профиль (группу между --new),
// в котором есть --ipset=. Профили без --ipset (hostlist) не трогаются.
func injectIpsetCFExclude(args []string, token string) []string {
	out := make([]string, 0, len(args)+4)
	group := make([]string, 0, 16)
	flush := func() {
		hasIpset, hasToken := false, false
		for _, a := range group {
			if strings.HasPrefix(a, "--ipset=") {
				hasIpset = true
			}
			if a == token {
				hasToken = true
			}
		}
		out = append(out, group...)
		if hasIpset && !hasToken {
			out = append(out, token)
		}
		group = group[:0]
	}
	for _, a := range args {
		if a == "--new" {
			flush()
			out = append(out, a)
			continue
		}
		group = append(group, a)
	}
	flush()
	return out
}

// applyIpsetCFCompat — точка входа: при установленном tg-ws добавляет
// исключение Cloudflare в ipset-правила.
func (m *Manager) applyIpsetCFCompat(args []string) []string {
	if !m.tgwsCompatOn() {
		return args
	}
	return injectIpsetCFExclude(args, "--ipset-exclude="+m.EnsureIpsetCFExclude())
}

// serviceArgsHaveIpsetCF — есть ли уже исключение Cloudflare в аргументах службы.
func (m *Manager) serviceArgsHaveIpsetCF() bool {
	out, err := winutil.HiddenCmd("reg", "query",
		`HKLM\SYSTEM\CurrentControlSet\Services\`+ServiceName, "/v", "ImagePath").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), ipsetCFExcludeName)
}

// IpsetCFMigrationNeeded — служба уже установлена, tg-ws установлен, но в её
// аргументах ещё нет исключения Cloudflare (например, после обновления GUI).
// Тогда службу надо один раз пересоздать.
func (m *Manager) IpsetCFMigrationNeeded() bool {
	if !m.Installed() || !m.tgwsCompatOn() {
		return false
	}
	if st, _ := ServiceState(); st == "" {
		return false // службы нет — её поставит обычный путь установки
	}
	return !m.serviceArgsHaveIpsetCF()
}
