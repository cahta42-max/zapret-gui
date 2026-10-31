//go:build darwin

package macutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// ProxyState — состояние системного прокси (ключи scutil --proxy).
type ProxyState struct {
	HTTPEnable         int
	HTTPProxy          string
	HTTPPort           int
	HTTPSEnable        int
	HTTPSProxy         string
	HTTPSPort          int
	SOCKSEnable        int
	ProxyAutoConfigEn  int
	ProxyAutoConfigURL string
}

// ReadProxy читает текущее состояние (scutil --proxy).
func ReadProxy() (ProxyState, error) {
	var st ProxyState
	out, err := Output("scutil", "--proxy")
	if err != nil {
		return st, err
	}
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, ":"); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			if k != "" {
				vals[k] = v
			}
		}
	}
	st.HTTPEnable, _ = strconv.Atoi(vals["HTTPEnable"])
	st.HTTPProxy = vals["HTTPProxy"]
	st.HTTPPort, _ = strconv.Atoi(vals["HTTPPort"])
	st.HTTPSEnable, _ = strconv.Atoi(vals["HTTPSEnable"])
	st.HTTPSProxy = vals["HTTPSProxy"]
	st.HTTPSPort, _ = strconv.Atoi(vals["HTTPSPort"])
	st.SOCKSEnable, _ = strconv.Atoi(vals["SOCKSEnable"])
	st.ProxyAutoConfigEn, _ = strconv.Atoi(vals["ProxyAutoConfigEnable"])
	st.ProxyAutoConfigURL = vals["ProxyAutoConfigURLString"]
	return st, nil
}

// ProxyActiveOn — включён ли системный прокси на указанный порт.
func ProxyActiveOn(port int) bool {
	st, err := ReadProxy()
	if err != nil {
		return false
	}
	return (st.HTTPEnable == 1 && st.HTTPPort == port) ||
		(st.HTTPSEnable == 1 && st.HTTPSPort == port)
}

// serviceProxy — сохранённое состояние прокси одного сетевого сервиса
// (для networksetup-фолбэка).
type serviceProxy struct {
	Name          string
	WebEnabled    bool
	WebServer     string
	WebPort       string
	SecureEnabled bool
	SecureServer  string
	SecurePort    string
}

// ProxyManager включает/выключает системный прокси, восстанавливая
// прежнее состояние. Сначала пробует scutil (без root — действует до смены
// сети), при отказе — networksetup через osascript (root, персистентно).
type ProxyManager struct {
	mu       sync.Mutex
	enabled  bool
	port     int
	snap     ProxyState
	services []serviceProxy // snapshot для networksetup
	usedNS   bool           // применили networksetup-фолбэк
}

func NewProxyManager() *ProxyManager { return &ProxyManager{} }

// Enabled — держим ли мы сейчас прокси включённым.
func (pm *ProxyManager) Enabled() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.enabled
}

// Enable включает HTTP/HTTPS-прокси на 127.0.0.1:port.
func (pm *ProxyManager) Enable(port int) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.enabled && pm.port == port {
		return nil
	}
	snap, err := ReadProxy()
	if err == nil && !pm.enabled {
		pm.snap = snap // снимок делаем один раз (до первого включения)
	}
	if err := pm.applyViaScutil(port); err == nil && ProxyActiveOn(port) {
		pm.enabled, pm.port = true, port
		return nil
	}
	// фолбэк: networksetup (нужен root — osascript спросит пароль)
	if err := pm.applyViaNetworksetup(port); err != nil {
		return err
	}
	pm.enabled, pm.port, pm.usedNS = true, port, true
	return nil
}

// Disable возвращает прежнее состояние прокси.
func (pm *ProxyManager) Disable() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if !pm.enabled {
		return nil
	}
	pm.enabled = false
	if pm.usedNS {
		return pm.restoreViaNetworksetup()
	}
	_ = pm.restoreViaScutil()
	return nil
}

// Ensure — периодическая проверка: если сеть сменилась и scutil-состояние
// сбросилось, включаем снова.
func (pm *ProxyManager) Ensure() {
	pm.mu.Lock()
	port, enabled := pm.port, pm.enabled
	pm.mu.Unlock()
	if enabled && !ProxyActiveOn(port) {
		_ = pm.Enable(port) // Enable сам не сделает повторный snapshot (enabled=true)
	}
}

// applyViaScutil пишет State:/Network/Global/Proxies напрямую.
func (pm *ProxyManager) applyViaScutil(port int) error {
	script := fmt.Sprintf(`open
get State:/Network/Global/Proxies
d.add HTTPEnable 1
d.add HTTPProxy 127.0.0.1
d.add HTTPPort %d
d.add HTTPSEnable 1
d.add HTTPSProxy 127.0.0.1
d.add HTTPSPort %d
d.add SOCKSEnable 0
d.add ProxyAutoConfigEnable 0
set State:/Network/Global/Proxies
quit
`, port, port)
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scutil set: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// restoreViaScutil возвращает значения из снимка (обычно — выключено).
func (pm *ProxyManager) restoreViaScutil() error {
	s := pm.snap
	script := fmt.Sprintf(`open
get State:/Network/Global/Proxies
d.add HTTPEnable %d
d.add HTTPProxy %s
d.add HTTPPort %d
d.add HTTPSEnable %d
d.add HTTPSProxy %s
d.add HTTPSPort %d
d.add SOCKSEnable %d
d.add ProxyAutoConfigEnable %d
set State:/Network/Global/Proxies
quit
`,
		s.HTTPEnable, orZeroIP(s.HTTPProxy), s.HTTPPort,
		s.HTTPSEnable, orZeroIP(s.HTTPSProxy), s.HTTPSPort,
		s.SOCKSEnable, s.ProxyAutoConfigEn)
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	_, err := cmd.CombinedOutput()
	return err
}

func orZeroIP(ip string) string {
	if ip == "" {
		return "0.0.0.0"
	}
	return ip
}

// listNetworkServices — активные сетевые сервисы (networksetup).
func listNetworkServices() []string {
	out, err := Output("networksetup", "-listallnetworkservices")
	if err != nil {
		return nil
	}
	var services []string
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == 0 { // заголовок
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "*") || strings.HasPrefix(line, "An asterisk") {
			continue
		}
		services = append(services, line)
	}
	return services
}

// applyViaNetworksetup — персистентная настройка прокси всех сервисов.
// Один osascript-запрос пароля на всё.
func (pm *ProxyManager) applyViaNetworksetup(port int) error {
	services := listNetworkServices()
	if len(services) == 0 {
		return fmt.Errorf("сетевые сервисы не найдены (networksetup)")
	}
	pm.snapshotServices(services)
	var buf bytes.Buffer
	for _, svc := range services {
		fmt.Fprintf(&buf, "networksetup -setwebproxy %s 127.0.0.1 %d; ", quoteShell(svc), port)
		fmt.Fprintf(&buf, "networksetup -setsecurewebproxy %s 127.0.0.1 %d; ", quoteShell(svc), port)
	}
	return runAdminScript(buf.String())
}

func (pm *ProxyManager) restoreViaNetworksetup() error {
	var buf bytes.Buffer
	for _, sp := range pm.services {
		state, secState := "off", "off"
		if sp.WebEnabled {
			state = "on"
		}
		if sp.SecureEnabled {
			secState = "on"
		}
		fmt.Fprintf(&buf, "networksetup -setwebproxystate %s %s; ", quoteShell(sp.Name), state)
		fmt.Fprintf(&buf, "networksetup -setsecurewebproxystate %s %s; ", quoteShell(sp.Name), secState)
		if sp.WebEnabled && sp.WebServer != "" {
			fmt.Fprintf(&buf, "networksetup -setwebproxy %s %s %s; ", quoteShell(sp.Name), quoteShell(sp.WebServer), quoteShell(sp.WebPort))
		}
		if sp.SecureEnabled && sp.SecureServer != "" {
			fmt.Fprintf(&buf, "networksetup -setsecurewebproxy %s %s %s; ", quoteShell(sp.Name), quoteShell(sp.SecureServer), quoteShell(sp.SecurePort))
		}
	}
	if buf.Len() == 0 {
		return nil
	}
	return runAdminScript(buf.String())
}

// snapshotServices читает текущие настройки прокси всех сервисов.
func (pm *ProxyManager) snapshotServices(services []string) {
	pm.services = nil
	for _, svc := range services {
		sp := serviceProxy{Name: svc}
		if out, err := Output("networksetup", "-getwebproxy", svc); err == nil {
			sp.WebEnabled, sp.WebServer, sp.WebPort = parseProxyAnswer(out)
		}
		if out, err := Output("networksetup", "-getsecurewebproxy", svc); err == nil {
			sp.SecureEnabled, sp.SecureServer, sp.SecurePort = parseProxyAnswer(out)
		}
		pm.services = append(pm.services, sp)
	}
}

func parseProxyAnswer(out string) (enabled bool, server, port string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Enabled:"):
			enabled = strings.Contains(line, "Yes")
		case strings.HasPrefix(line, "Server:"):
			server = strings.TrimSpace(strings.TrimPrefix(line, "Server:"))
		case strings.HasPrefix(line, "Port:"):
			port = strings.TrimSpace(strings.TrimPrefix(line, "Port:"))
		}
	}
	return
}

// runAdminScript выполняет shell-команды от root (один запрос пароля).
func runAdminScript(shell string) error {
	script := `do shell script "` + escapeAppleScript(shell) + `" with administrator privileges with prompt "Zapret GUI: настройка системного прокси"`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
