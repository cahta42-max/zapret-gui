//go:build darwin

// Package utunws управляет движком DPI-обхода utunws из ZapretMac
// (Flowseal/zapret-mac-discord-youtube, официальный macOS-порт
// nfqws). Движок — системный: LaunchDaemon (root) + pf anchor + туннель
// utun50. Наш GUI: скачивает релиз, ставит через install.sh (osascript),
// управляет стратегией/ipset файлами в ~/Library/Application Support/
// ZapretMac и дёргает restart.sh/stop.sh. Подробности —
// docs/macos-engine-utunws.md.
package utunws

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"zapret_gui/internal/macutil"
	"zapret_gui/internal/release"
)

// DaemonLabel — метка LaunchDaemon движка.
const DaemonLabel = "system/io.github.flowseal.zapretmac"

// BaseDir — системная копия дистрибутива (root, rsync из install.sh).
func BaseDir() string { return "/Library/Application Support/ZapretMac" }

// DataRoot — пользовательские данные движка (стратегия, ipset, списки).
func DataRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "ZapretMac")
}

// Manager — управляем движком через скрипты ZapretMac.
type Manager struct {
	distDir string // tools/zapretmac — распакованный релиз (ZapretMac.app)
	onLog   func(string)
}

func New(toolsDir string, onLog func(string)) *Manager {
	return &Manager{distDir: filepath.Join(toolsDir, "zapretmac"), onLog: onLog}
}

// DistDir — каталог распакованного релиза.
func (m *Manager) DistDir() string { return m.distDir }

// PayloadSrc — каталог Payload внутри .app-бандла (bin/, strategies/,
// скрипты). Если структура релиза изменится — откатываемся на сам distDir.
func (m *Manager) PayloadSrc() string {
	p := filepath.Join(m.distDir, "Contents", "Resources", "Payload")
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p
	}
	if st, err := os.Stat(filepath.Join(m.distDir, "ZapretMac.app")); err == nil && st.IsDir() {
		// Unzip мог не срезать корень (несколько корневых папок) — редко
		return filepath.Join(m.distDir, "ZapretMac.app", "Contents", "Resources", "Payload")
	}
	return m.distDir
}

// PlistPath — LaunchDaemon-файл движка.
func (m *Manager) PlistPath() string {
	return "/Library/LaunchDaemons/io.github.flowseal.zapretmac.plist"
}

// Installed — поставлен ли движок (plist + бинарь utunws).
func (m *Manager) Installed() bool {
	if _, err := os.Stat(m.PlistPath()); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(BaseDir(), "bin", "utunws"))
	return err == nil
}

// Downloaded — скачан ли дистрибутив (для показа версий без установки).
func (m *Manager) Downloaded() bool {
	_, err := os.Stat(filepath.Join(m.PayloadSrc(), "install.sh"))
	return err == nil
}

var plistVerRe = regexp.MustCompile(`(?s)CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)

// Version — версия установленного дистрибутива ZapretMac (из Info.plist
// нашей копии; после установки совпадает с системной).
func (m *Manager) Version() string {
	if v := readVersionFile(); v != "" {
		return v
	}
	data, err := os.ReadFile(filepath.Join(m.distDir, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	if mm := plistVerRe.FindStringSubmatch(string(data)); mm != nil {
		return strings.TrimSpace(mm[1])
	}
	return ""
}

func readVersionFile() string {
	data, err := os.ReadFile(filepath.Join(DataRoot(), "installed-version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ---------------------------------------------------------------- состояние

// Running — работает ли движок (туннель utun50 поднят; run.sh создаёт
// его только при живом utunws, stop.sh гарантированно убирает).
func (m *Manager) Running() bool {
	return utunUp()
}

func utunUp() bool {
	return exec.Command("/sbin/ifconfig", "utun50").Run() == nil
}

// waitDown/waitUp — ожидание исчезновения/появления туннеля.
func waitDown(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !utunUp() {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return !utunUp()
}

func waitUp(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if utunUp() {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return utunUp()
}

// DaemonState — текстовое состояние LaunchDaemon (диагностика).
func (m *Manager) DaemonState() string {
	out, err := exec.Command("/bin/launchctl", "print", DaemonLabel).Output()
	if err != nil {
		return "не загружен"
	}
	state := "загружен"
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			state = strings.TrimPrefix(line, "state = ")
			break
		}
	}
	if strings.Contains(string(out), "disabled") {
		state += " (выключен автозапуск)"
	}
	return state
}

// SelectedStrategy — id стратегии из DataRoot (без пробелов).
func (m *Manager) SelectedStrategy() string {
	data, err := os.ReadFile(filepath.Join(DataRoot(), "selected-strategy"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// IpsetMode — режим ipset (none | loaded | any; пусто = none).
func (m *Manager) IpsetMode() string {
	data, err := os.ReadFile(filepath.Join(DataRoot(), "ipset-mode"))
	if err != nil {
		return "none"
	}
	v := strings.TrimSpace(string(data))
	switch v {
	case "none", "loaded", "any":
		return v
	default:
		return "none"
	}
}

// SetStrategy пишет выбранную стратегию (сам файл — без root; применяется
// перезапуском движка).
func (m *Manager) SetStrategy(id string) error {
	if id == "" {
		id = "general"
	}
	if err := os.MkdirAll(DataRoot(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(DataRoot(), "selected-strategy"), []byte(id+"\n"), 0o644)
}

// SetIpsetMode пишет режим ipset (применяется перезапуском движка).
func (m *Manager) SetIpsetMode(mode string) error {
	switch mode {
	case "none", "loaded", "any":
	default:
		return fmt.Errorf("неизвестный режим ipset: %s", mode)
	}
	if err := os.MkdirAll(DataRoot(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(DataRoot(), "ipset-mode"), []byte(mode+"\n"), 0o644)
}

// ---------------------------------------------------------------- установка

// Install скачивает дистрибутив ZapretMac (tag="" → последний релиз),
// готовит DataRoot и ставит движок через install.sh (один osascript-
// запрос пароля). После установки движок сразу запущен.
func (m *Manager) Install(tag string, progress func(pct float64, stage string)) (string, error) {
	emit := func(pct float64, stage string) {
		if progress != nil {
			progress(pct, stage)
		}
	}
	ctx := context.Background()

	// 1. релиз
	emit(0.02, "Ищу релиз ZapretMac…")
	var asset release.Asset
	var err error
	if tag == "" {
		tag, asset, err = release.LatestZapretMac(ctx)
	} else {
		asset, err = release.ZapretMacAsset(ctx, tag)
	}
	if err != nil {
		return "", err
	}

	// 2. скачивание
	emit(0.08, "Скачиваю ZapretMac "+tag+"…")
	tmpZip := filepath.Join(os.TempDir(), "zapretgui-zapretmac.zip")
	if err := release.Download(ctx, asset.URL, tmpZip, func(sent, total int64) {
		if total > 0 {
			emit(0.08+float64(sent)/float64(total)*0.62, "Скачиваю ZapretMac "+tag+"…")
		}
	}); err != nil {
		return "", err
	}
	defer os.Remove(tmpZip)

	// 3. распаковка в tools/zapretmac
	emit(0.75, "Распаковываю…")
	os.RemoveAll(m.distDir)
	if err := os.MkdirAll(m.distDir, 0o755); err != nil {
		return "", err
	}
	if err := release.Unzip(tmpZip, m.distDir); err != nil {
		return "", err
	}
	src := m.PayloadSrc()
	if _, err := os.Stat(filepath.Join(src, "install.sh")); err != nil {
		return "", fmt.Errorf("в релизе %s нет install.sh (структура изменилась?)", tag)
	}

	// 4. пользовательские данные: списки (не трогаем существующие),
	//    стратегия и ipset по умолчанию
	emit(0.82, "Готовлю списки…")
	if err := m.prepareDataRoot(); err != nil {
		return "", err
	}

	// 5. install.sh от root: копирует payload в систему, ставит
	//    LaunchDaemon, применяет keepinit и стартует движок; следом ТЕМ ЖЕ
	//    заходом — обёртки+sudoers, чтобы дальше паролей не было вовсе
	emit(0.88, "Устанавливаю движок (нужен пароль администратора)…")
	installLog := filepath.Join(os.TempDir(), "zapretgui-install.log")
	shell := fmt.Sprintf("sh %s %s %s > %s 2>&1",
		shq(filepath.Join(src, "install.sh")), shq(src), shq(DataRoot()), shq(installLog))
	if err := macutil.RunAdminSync("Zapret GUI: установка движка обхода (utunws)", shell); err != nil {
		return tag, fmt.Errorf("%v\n%s", err, tailFile(installLog, 15))
	}
	// sudoers отдельным osascript: отказ не должен ронять установку
	if err := macutil.InstallPasswordless("Zapret GUI: настройка без пароля (последний раз)"); err != nil {
		if m.onLog != nil {
			m.onLog("[движок] безпарольный режим не настроен: " + err.Error())
		}
	}

	// 6. проверяем, что туннель поднялся
	if !waitUp(15 * time.Second) {
		return tag, fmt.Errorf("движок установлен, но туннель utun50 не поднялся:\n%s", m.LogTail(15))
	}
	_ = os.WriteFile(filepath.Join(DataRoot(), "installed-version"), []byte(tag+"\n"), 0o644)
	emit(1, "Готово")
	if m.onLog != nil {
		m.onLog("[движок] utunws " + tag + " установлен и запущен")
	}
	return tag, nil
}

// prepareDataRoot создаёт ~/Library/Application Support/ZapretMac:
// списки (первый раз — из default-lists; существующие не трогаем —
// там правки пользователя) и файлы выбора по умолчанию.
func (m *Manager) prepareDataRoot() error {
	src := m.PayloadSrc()
	defaults := filepath.Join(src, "default-lists")
	lists := filepath.Join(DataRoot(), "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(defaults)
	if err != nil {
		return fmt.Errorf("default-lists не найдены: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(lists, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // уже есть (обновление) — сохраняем правки юзера
		}
		data, err := os.ReadFile(filepath.Join(defaults, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(DataRoot(), "selected-strategy")); err != nil {
		if err := m.SetStrategy("general"); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(DataRoot(), "ipset-mode")); err != nil {
		if err := m.SetIpsetMode("none"); err != nil {
			return err
		}
	}
	return nil
}

// RefreshLists обновляет НЕпользовательские списки из default-lists
// (аналог «обновить ipset» на Windows). Файлы *-user.txt не трогаем.
func (m *Manager) RefreshLists() (int, error) {
	defaults := filepath.Join(m.PayloadSrc(), "default-lists")
	lists := filepath.Join(DataRoot(), "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(defaults)
	if err != nil {
		return 0, fmt.Errorf("default-lists не найдены: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), "-user.txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(defaults, e.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(lists, e.Name()), data, 0o644); err == nil {
			n++
		}
	}
	return n, nil
}

// ListsDir — каталог списков пользователя.
func (m *Manager) ListsDir() string { return filepath.Join(DataRoot(), "lists") }

// ---------------------------------------------------------------- управление

// ensureNopass гарантирует (по возможности) безпарольный режим:
// первым действием Старт/Стоп ставит обёртки+sudoers одним osascript
// («последний пароль»). Отказ юзера/ошибка — не фатально: работаем
// по-старому, с паролем на каждое действие.
func ensureNopass() {
	if macutil.PasswordlessAvailable() {
		return
	}
	_ = macutil.InstallPasswordless("Zapret GUI: настройка без пароля (последний раз)")
}

// Start запускает/перезапускает движок (restart.sh: enable + bootstrap
// при необходимости + kickstart). Применяет текущие selected-strategy/
// ipset-mode из DataRoot.
func (m *Manager) Start() error {
	if !m.Installed() {
		return fmt.Errorf("движок не установлен")
	}
	ensureNopass()
	if macutil.PasswordlessAvailable() {
		if err := macutil.RunPasswordless("engine-restart"); err != nil {
			return err
		}
	} else {
		restartLog := filepath.Join(os.TempDir(), "zapretgui-restart.log")
		shell := fmt.Sprintf("sh %s > %s 2>&1",
			shq(filepath.Join(BaseDir(), "restart.sh")), shq(restartLog))
		if err := macutil.RunAdminSync("Zapret GUI: запуск движка обхода", shell); err != nil {
			return fmt.Errorf("%v\n%s", err, tailFile(restartLog, 5))
		}
	}
	// kickstart -k убивает старый run.sh: ждём пересоздания туннеля
	waitDown(4 * time.Second)
	if !waitUp(15 * time.Second) {
		return fmt.Errorf("туннель utun50 не поднялся:\n%s", m.LogTail(15))
	}
	// run.sh после появления туннеля настраивает ifconfig и pf-правила —
	// даём осесть, иначе первый запрос уходит мимо перехвата
	time.Sleep(700 * time.Millisecond)
	if m.onLog != nil {
		m.onLog("[движок] utunws запущен (стратегия " + m.SelectedStrategy() + ", ipset " + m.IpsetMode() + ")")
	}
	return nil
}

// Stop останавливает движок и снимает перехват (stop.sh: демоны, pf,
// keepinit). Автозапуск при загрузке отключается до следующего старта.
func (m *Manager) Stop() error {
	if !m.Installed() {
		return nil
	}
	ensureNopass()
	if macutil.PasswordlessAvailable() {
		if err := macutil.RunPasswordless("engine-stop"); err != nil {
			return err
		}
	} else {
		stopLog := filepath.Join(os.TempDir(), "zapretgui-stop.log")
		shell := fmt.Sprintf("sh %s > %s 2>&1",
			shq(filepath.Join(BaseDir(), "stop.sh")), shq(stopLog))
		if err := macutil.RunAdminSync("Zapret GUI: остановка движка обхода", shell); err != nil {
			return fmt.Errorf("%v\n%s", err, tailFile(stopLog, 5))
		}
	}
	if !waitDown(10 * time.Second) {
		return fmt.Errorf("туннель utun50 не исчез после stop.sh")
	}
	if m.onLog != nil {
		m.onLog("[движок] utunws остановлен")
	}
	return nil
}

// ---------------------------------------------------------------- логи

// LogTail — последние n строк engine.log движка (root пишет 0644).
func (m *Manager) LogTail(n int) string {
	return tailFile(filepath.Join(BaseDir(), "engine.log"), n)
}

func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > 4000 { // не тащим мегабайты в GUI
		out = "…" + out[len(out)-4000:]
	}
	return out
}

// shq — одинарное экранирование для sh.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
