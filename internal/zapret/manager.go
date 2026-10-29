// Package zapret управляет сервисом zapret-discord-youtube:
// запуск стратегий как процесса, установка/удаление службы Windows,
// переключатели Game Filter / IPSet / Check Updates, обновление дистрибутива.
//go:build windows

package zapret

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"zapret_gui/internal/release"
	"zapret_gui/internal/winutil"
)

const ServiceName = "zapret"

// Manager управляет дистрибутивом zapret в заданном каталоге.
type Manager struct {
	dir   string        // корень дистрибутива (где лежит general.bat)
	mu    chan struct{} // семафор запуска/остановки
	cmd   *exec.Cmd
	done  chan struct{}
	onLog func(line string)
}

func New(dir string, onLog func(string)) *Manager {
	return &Manager{dir: dir, mu: make(chan struct{}, 1), onLog: onLog}
}

func (m *Manager) Dir() string { return m.dir }

// Installed — есть ли дистрибутив (наличие bin\winws.exe).
func (m *Manager) Installed() bool {
	_, err := os.Stat(m.winwsPath())
	return err == nil
}

func (m *Manager) winwsPath() string { return filepath.Join(m.dir, "bin", "winws.exe") }

var localVersionRe = regexp.MustCompile(`(?i)set\s+"?LOCAL_VERSION=([0-9a-z.]+)"?`)

// Version возвращает версию дистрибутива: из .service/version.txt (если есть)
// либо из LOCAL_VERSION в service.bat (в релизном zip .service отсутствует).
func (m *Manager) Version() string {
	if v := release.ReadVersionFile(m.dir); v != "" {
		return v
	}
	data, err := os.ReadFile(filepath.Join(m.dir, "service.bat"))
	if err != nil {
		return ""
	}
	if m := localVersionRe.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// BuildVars собирает переменные для текущего состояния переключателей.
func (m *Manager) BuildVars() Vars {
	tcp, udp := GameFilterPorts(m.GameFilterMode())
	return Vars{
		BinDir:        filepath.Join(m.dir, "bin") + string(os.PathSeparator),
		ListsDir:      filepath.Join(m.dir, "lists") + string(os.PathSeparator),
		RootDir:       m.dir + string(os.PathSeparator),
		GameFilterTCP: tcp,
		GameFilterUDP: udp,
	}
}

// CommandArgs возвращает argv для запуска winws.exe с выбранной стратегией.
func (m *Manager) CommandArgs(strategy string) ([]string, error) {
	list, err := ScanStrategies(m.dir)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		if s.Name == strategy {
			return append([]string{m.winwsPath()}, ExpandArgs(s.RawArgs, m.BuildVars())...), nil
		}
	}
	return nil, fmt.Errorf("стратегия %q не найдена", strategy)
}

// ---------------------------------------------------------------- process

// EnsureUserLists создаёт пользовательские списки, если их нет (аналог
// :load_user_lists в service.bat — без этих файлов winws не стартует:
// «cannot access ipset file ...ipset-exclude-user.txt»).
func (m *Manager) EnsureUserLists() {
	lists := filepath.Join(m.dir, "lists")
	_ = os.MkdirAll(lists, 0o755)
	ipsetExcl := filepath.Join(lists, "ipset-exclude-user.txt")
	if _, err := os.Stat(ipsetExcl); os.IsNotExist(err) {
		_ = os.WriteFile(ipsetExcl, []byte("203.0.113.113/32\r\n"), 0o644)
	}
	genUser := filepath.Join(lists, "list-general-user.txt")
	if _, err := os.Stat(genUser); os.IsNotExist(err) {
		_ = os.WriteFile(genUser, []byte("# Never leave this file empty\r\ndomain.example.abc\r\n"), 0o644)
	}
	exclUser := filepath.Join(lists, "list-exclude-user.txt")
	if _, err := os.Stat(exclUser); os.IsNotExist(err) {
		_ = os.WriteFile(exclUser, []byte("domain.example.abc\r\n"), 0o644)
	}
}

// userListFiles — пользовательские списки, редактируемые из GUI.
// Ключи стабильны: по ним строятся Wails-биндинги и разметка фронтенда.
var userListFiles = map[string]string{
	"general":       "list-general-user.txt",  // домены для обработки (маскировка SNI)
	"exclude":       "list-exclude-user.txt",  // домены-исключения
	"ipset-exclude": "ipset-exclude-user.txt", // IP/CIDR-исключения
}

// userListPlaceholders пишутся вместо «пустого» списка: winws читает
// файлы только на старте, и пустой/без строк hostlist — риск падения
// (см. EnsureUserLists и комментарий дистрибутива «Never leave this file empty»).
var userListPlaceholders = map[string]string{
	"general":       "# Never leave this file empty\r\ndomain.example.abc\r\n",
	"exclude":       "domain.example.abc\r\n",
	"ipset-exclude": "203.0.113.113/32\r\n",
}

// UserListKinds возвращает поддерживаемые kind'ы пользовательских списков
// (в стабильном порядке для UI).
func UserListKinds() []string {
	return []string{"general", "exclude", "ipset-exclude"}
}

// UserListPath возвращает путь к пользовательскому списку kind.
func (m *Manager) UserListPath(kind string) (string, error) {
	name, ok := userListFiles[kind]
	if !ok {
		return "", fmt.Errorf("неизвестный список %q", kind)
	}
	return filepath.Join(m.dir, "lists", name), nil
}

// ReadUserList читает пользовательский список; отсутствующий файл — пустая строка
// (фронт показывает заглушку-подсказку вместо пустоты).
func (m *Manager) ReadUserList(kind string) (string, error) {
	p, err := m.UserListPath(kind)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var reUserListDomain = regexp.MustCompile(`^[a-zA-Z0-9.^*_\-]+$`)
var reUserListIp = regexp.MustCompile(`^[0-9a-fA-F.:/]+$`)

func validateUserListLine(kind, ln string) error {
	if kind == "ipset-exclude" {
		if !reUserListIp.MatchString(ln) {
			return fmt.Errorf("ожидался IP или CIDR, получено %q", ln)
		}
		if strings.Contains(ln, "/") {
			if _, _, err := net.ParseCIDR(ln); err != nil {
				return fmt.Errorf("некорректный CIDR %q: %v", ln, err)
			}
		} else if net.ParseIP(ln) == nil {
			return fmt.Errorf("некорректный IP %q", ln)
		}
		return nil
	}
	// домены: разрешён синтаксис списков zapret (^префикс, *суффикс, .tld)
	if !reUserListDomain.MatchString(ln) {
		return fmt.Errorf("недопустимый домен %q (разрешены буквы, цифры, точки, дефисы, ^ и *)", ln)
	}
	return nil
}

// WriteUserList перезаписывает пользовательский список с построчной
// валидацией. Комментарии (#) и пустые строки сохраняются. Если «живых»
// строк нет — пишется заглушка (это и есть сброс списка к дефолту).
func (m *Manager) WriteUserList(kind, text string) error {
	p, err := m.UserListPath(kind)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	hasContent := false
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			out = append(out, ln)
			continue
		}
		if err := validateUserListLine(kind, ln); err != nil {
			return fmt.Errorf("строка %d: %w", i+1, err)
		}
		out = append(out, ln)
		hasContent = true
	}
	if !hasContent {
		out = []string{strings.TrimSpace(userListPlaceholders[kind])}
	}
	return os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// placeholderUserLines — строки-заглушки списков дистрибутива: в GUI не
// показываются и не мешают winws (их генерирует EnsureUserLists/WriteUserList).
var placeholderUserLines = map[string]bool{
	"domain.example.abc":            true,
	"203.0.113.113/32":              true,
	"# never leave this file empty": true,
}

// GetUserDomains возвращает пользовательские списки как домены по kind'ам
// (без комментариев, пустых строк и заглушек дистрибутива — GUI показывает
// только живые записи юзера).
func (m *Manager) GetUserDomains() (map[string][]string, error) {
	out := make(map[string][]string, len(userListFiles))
	for kind := range userListFiles {
		raw, err := m.ReadUserList(kind)
		if err != nil {
			return nil, err
		}
		domains := []string{}
		for _, ln := range strings.Split(raw, "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "#") || placeholderUserLines[strings.ToLower(ln)] {
				continue
			}
			domains = append(domains, ln)
		}
		out[kind] = domains
	}
	return out, nil
}

// SetUserDomains записывает домены по kind'ам (только переданные).
// Пустой список → заглушка-файл: winws продолжит работать с дефолтом.
func (m *Manager) SetUserDomains(domains map[string][]string) error {
	for kind, list := range domains {
		if err := m.WriteUserList(kind, strings.Join(list, "\n")); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
	}
	return nil
}

// Start запускает winws.exe как фоновый процесс с выбранной стратегией.
func (m *Manager) Start(strategy string) error {
	if !m.Installed() {
		return errors.New("zapret не установлен")
	}
	m.EnsureUserLists()
	if err := m.StopProcess(); err != nil {
		return err
	}
	// если работает служба — останавливаем её, иначе winws не запустится
	if st, _ := ServiceState(); st == "RUNNING" || st == "START_PENDING" {
		ServiceStop()
	}

	argv, err := m.CommandArgs(strategy)
	if err != nil {
		return err
	}

	m.mu <- struct{}{}
	defer func() { <-m.mu }()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = filepath.Dir(argv[0])
	cmd.SysProcAttr = hideWindow()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.done = make(chan struct{})

	go m.pumpLog(stdout)
	go m.pumpLog(stderr)
	go func() {
		cmd.Wait()
		close(m.done)
		if m.onLog != nil {
			m.onLog(fmt.Sprintf("[zapret] процесс завершён (код %d)", cmd.ProcessState.ExitCode()))
		}
	}()
	if m.onLog != nil {
		m.onLog(fmt.Sprintf("[zapret] стратегия «%s» запущена", strategy))
	}
	return nil
}

func (m *Manager) pumpLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		if m.onLog != nil {
			m.onLog("[zapret] " + sc.Text())
		}
	}
}

// ProcessRunning — жив ли запущенный нами winws.
func (m *Manager) ProcessRunning() bool {
	if m.cmd == nil {
		return false
	}
	select {
	case <-m.done:
		return false
	default:
		return true
	}
}

// StopProcess останавливает winws.exe (и наш, и чужой — как taskkill в bat).
func (m *Manager) StopProcess() error {
	m.mu <- struct{}{}
	defer func() { <-m.mu }()
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		<-m.done
		m.cmd = nil
	}
	// чужие экземпляры
	if isProcessRunning("winws.exe") {
		if err := winutil.HiddenCmd("taskkill", "/IM", "winws.exe", "/F").Run(); err != nil {
			return err
		}
	}
	return nil
}

func hideWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func isProcessRunning(name string) bool {
	out, err := winutil.HiddenCmd("tasklist", "/FI", "IMAGENAME eq "+name).Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(name))
}

// ---------------------------------------------------------------- service

var scStateRe = regexp.MustCompile(`(?i)\b(STOPPED|START_PENDING|RUNNING|STOP_PENDING|PAUSED|CONTINUE_PENDING)\b`)

// ServiceState возвращает состояние службы zapret ("" если не установлена).
// Регэксп матчит ЗНАЧЕНИЕ состояния, а не имя поля: на русской Windows
// sc выводит «Состояние : 4 RUNNING» (имя поля локализовано, а значение —
// всегда английское), поэтому парсить «STATE:» нельзя — ловили пустое
// состояние при работающей службе.
func ServiceState() (string, error) {
	out, err := winutil.HiddenCmd("sc", "query", ServiceName).Output()
	if err != nil {
		// служба не установлена — sc завершается с ошибкой 1060
		return "", err
	}
	if m := scStateRe.FindStringSubmatch(string(out)); m != nil {
		return strings.ToUpper(m[1]), nil
	}
	return "", nil
}

var scExitRe = regexp.MustCompile(`WIN32_EXIT_CODE\s*:\s*(\d+)`)

// ServiceExitCode возвращает код выхода службы из `sc query` (0 — штатно).
// 433 = ERROR_DEVICE_DOES_NOT_EXIST: winws не смог открыть WinDivert-устройство
// (типично после обновления дистрибутива на работающем GUI).
func ServiceExitCode() (uint32, error) {
	out, err := winutil.HiddenCmd("sc", "query", ServiceName).Output()
	if err != nil {
		return 0, err
	}
	return parseServiceExitCode(string(out)), nil
}

// parseServiceExitCode вынесен для тестов: имена полей sc локализует, но
// WIN32_EXIT_CODE и число всегда выводятся латиницей.
func parseServiceExitCode(out string) uint32 {
	m := scExitRe.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

// ServiceStrategy читает имя стратегии из реестра (как service.bat).
func ServiceStrategy() string {
	out, err := winutil.HiddenCmd("reg", "query",
		`HKLM\System\CurrentControlSet\Services\`+ServiceName,
		"/v", "zapret-discord-youtube").Output()
	if err != nil {
		return ""
	}
	// ... REG_SZ general (ALT2)
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		if i := strings.Index(l, "REG_SZ"); i >= 0 {
			return strings.TrimSpace(l[i+len("REG_SZ"):])
		}
	}
	return ""
}

// ServiceInstall устанавливает службу zapret с выбранной стратегией (аналог
// service.bat → Install Service): sc create + start + пометка стратегии.
func (m *Manager) ServiceInstall(strategy string) error {
	if !m.Installed() {
		return errors.New("zapret не установлен")
	}
	_ = m.StopProcess()
	m.EnsureUserLists() // без пользовательских списков служба упадёт на старте

	argv, err := m.CommandArgs(strategy)
	if err != nil {
		return err
	}
	binPath := strings.Join(quoteIfNeeded(argv), " ")

	tcpEnable()

	_ = winutil.HiddenCmd("net", "stop", ServiceName).Run()
	_ = winutil.HiddenCmd("sc", "delete", ServiceName).Run()

	if out, err := winutil.HiddenCmd("sc", "create", ServiceName,
		"binPath=", binPath,
		"DisplayName=", "zapret",
		"start=", "auto").CombinedOutput(); err != nil {
		return fmt.Errorf("sc create: %v: %s", err, strings.TrimSpace(string(out)))
	}
	_ = winutil.HiddenCmd("sc", "description", ServiceName, "Zapret DPI bypass software").Run()
	if out, err := winutil.HiddenCmd("sc", "start", ServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("sc start: %v: %s", err, strings.TrimSpace(string(out)))
	}
	_ = winutil.HiddenCmd("reg", "add",
		`HKLM\System\CurrentControlSet\Services\`+ServiceName,
		"/v", "zapret-discord-youtube", "/t", "REG_SZ", "/d", strategy, "/f").Run()
	if m.onLog != nil {
		m.onLog(fmt.Sprintf("[zapret] служба установлена со стратегией «%s»", strategy))
	}
	return nil
}

// quoteIfNeeded обрамляет кавычками argv-токены с пробелами (для sc binPath).
func quoteIfNeeded(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t") {
			out[i] = `"` + a + `"`
		} else {
			out[i] = a
		}
	}
	return out
}

// ServiceStop останавливает службу zapret и ДОЖИДАЕТСЬ фактической
// остановки (до 15 с) — иначе «остановили», а SCM ещё держит процесс.
func ServiceStop() error {
	st, _ := ServiceState()
	if st != "RUNNING" && st != "START_PENDING" {
		return nil // уже остановлена (или не установлена)
	}
	if out, err := winutil.HiddenCmd("net", "stop", ServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("net stop: %v: %s", err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := ServiceState()
		if st == "STOPPED" || st == "" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("служба не остановилась за 15 с")
}

// EnsureRunning гарантирует, что zapret работает со стратегией strategy и
// текущими переключателями (gamefilter/ipset — файлы дистрибутива, они уже
// сохранены). Вызывается при автозапуске GUI. Предпочтение отдаётся службе:
// она стартует сама при загрузке Windows, ещё до входа пользователя.
func (m *Manager) EnsureRunning(strategy string) error {
	if !m.Installed() {
		return errors.New("zapret не установлен")
	}
	m.EnsureUserLists()
	if strategy == "" { // конфиг без стратегии — берём дефолтный general
		if list, err := ScanStrategies(m.dir); err == nil {
			for _, s := range list {
				if s.Name == "general" {
					strategy = s.Name
					break
				}
			}
			if strategy == "" && len(list) > 0 {
				strategy = list[0].Name
			}
		}
	}
	if strategy == "" {
		return errors.New("стратегии не найдены")
	}

	st, _ := ServiceState()
	if st == "RUNNING" || st == "START_PENDING" {
		// служба уже работает: если пользователь потом выбрал другую
		// стратегию — пересоздаём с последними сохранёнными параметрами
		if s := ServiceStrategy(); s != "" && s != strategy {
			return m.ServiceInstall(strategy)
		}
		return nil
	}
	if st == "" {
		// службы нет — устанавливаем (start=auto + запуск)
		return m.ServiceInstall(strategy)
	}
	// установлена, но остановлена: при смене стратегии — пересоздаём
	if s := ServiceStrategy(); s != "" && s != strategy {
		return m.ServiceInstall(strategy)
	}
	// чужой winws может держать WinDivert — прибираем и стартуем службу
	_ = m.StopProcess()
	if out, err := winutil.HiddenCmd("net", "start", ServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("net start: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if m.onLog != nil {
		m.onLog("[zapret] служба запущена автоматически (стратегия «" + strategy + "»)")
	}
	return nil
}

// ServiceRemove полностью удаляет службу и хвосты WinDivert (аналог Remove Services).
func ServiceRemove() error {
	_ = winutil.HiddenCmd("net", "stop", ServiceName).Run()
	if out, err := winutil.HiddenCmd("sc", "delete", ServiceName).CombinedOutput(); err != nil &&
		!strings.Contains(strings.ToLower(string(out)), "не существует") &&
		!strings.Contains(strings.ToLower(string(out)), "does not exist") {
		return fmt.Errorf("sc delete: %s", strings.TrimSpace(string(out)))
	}
	// чужие winws
	_ = winutil.HiddenCmd("taskkill", "/IM", "winws.exe", "/F").Run()
	// WinDivert
	for _, svc := range []string{"WinDivert", "WinDivert14"} {
		_ = winutil.HiddenCmd("net", "stop", svc).Run()
		_ = winutil.HiddenCmd("sc", "delete", svc).Run()
	}
	return nil
}

// tcpEnable включает TCP timestamps (как :tcp_enable в service.bat).
func tcpEnable() {
	_ = winutil.HiddenCmd("netsh", "interface", "tcp", "set", "global", "timestamps=enabled").Run()
}

// ---------------------------------------------------------------- toggles

func (m *Manager) gameFlagPath() string {
	return filepath.Join(m.dir, "utils", "game_filter.enabled")
}

// GameFilterMode возвращает "off" | "all" | "tcp" | "udp".
func (m *Manager) GameFilterMode() string {
	data, err := os.ReadFile(m.gameFlagPath())
	if err != nil {
		return "off"
	}
	mode := strings.ToLower(strings.TrimSpace(string(data)))
	switch mode {
	case "all", "tcp", "udp":
		return mode
	default:
		return "udp" // как в bat: неизвестное значение → ветка else (UDP)
	}
}

// GameFilterPorts пересчитывает режим в порты (12 = заглушка "выключено").
func GameFilterPorts(mode string) (tcp, udp string) {
	switch mode {
	case "all":
		return "1024-65535", "1024-65535"
	case "tcp":
		return "1024-65535", "12"
	case "udp":
		return "12", "1024-65535"
	default:
		return "12", "12"
	}
}

// SetGameFilter устанавливает режим ("off" удаляет флаг-файл).
func (m *Manager) SetGameFilter(mode string) error {
	path := m.gameFlagPath()
	if mode == "off" {
		return os.Remove(path)
	}
	switch mode {
	case "all", "tcp", "udp":
		return os.WriteFile(path, []byte(mode), 0o644)
	}
	return fmt.Errorf("неизвестный режим: %s", mode)
}

// CheckUpdatesEnabled — состояние utils/check_updates.enabled.
func (m *Manager) CheckUpdatesEnabled() bool {
	_, err := os.Stat(filepath.Join(m.dir, "utils", "check_updates.enabled"))
	return err == nil
}

func (m *Manager) SetCheckUpdates(enabled bool) error {
	path := filepath.Join(m.dir, "utils", "check_updates.enabled")
	if enabled {
		return os.WriteFile(path, []byte("ENABLED"), 0o644)
	}
	return os.Remove(path)
}

// IpsetStatus: "none" (заглушка), "any" (пустой файл), "loaded" (реальный список).
func (m *Manager) IpsetStatus() string {
	data, err := os.ReadFile(filepath.Join(m.dir, "lists", "ipset-all.txt"))
	if err != nil {
		return "none"
	}
	if len(strings.Fields(string(data))) == 0 {
		return "any"
	}
	if strings.Contains(string(data), "203.0.113.113/32") {
		return "none"
	}
	return "loaded"
}

// SetIpset переключает режим IPSet с сохранением реального списка в .backup
// (переносим логику :ipset_switch из service.bat).
func (m *Manager) SetIpset(desired string) error {
	list := filepath.Join(m.dir, "lists", "ipset-all.txt")
	backup := list + ".backup"
	current := m.IpsetStatus()
	if current == desired {
		return nil
	}
	switch desired {
	case "none":
		if current == "loaded" { // сохраняем реальный список
			_ = os.Remove(backup)
			if err := os.Rename(list, backup); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return os.WriteFile(list, []byte("203.0.113.113/32\r\n"), 0o644)
	case "any":
		return os.WriteFile(list, nil, 0o644)
	case "loaded":
		if _, err := os.Stat(backup); err != nil {
			return errors.New("нет сохранённого списка — сначала обновите IPSet")
		}
		_ = os.Remove(list)
		return os.Rename(backup, list)
	}
	return fmt.Errorf("неизвестный режим: %s", desired)
}

// UpdateIpset скачивает свежий ipset-all из репозитория (аналог Update IPSet List).
func (m *Manager) UpdateIpset() error {
	url := "https://raw.githubusercontent.com/Flowseal/zapret-discord-youtube/main/.service/ipset-service.txt"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ipset-service.txt: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	list := filepath.Join(m.dir, "lists", "ipset-all.txt")
	_ = os.Remove(list + ".backup")
	_ = os.Rename(list, list+".backup") // старый список — в бэкап
	return os.WriteFile(list, body, 0o644)
}

// ---------------------------------------------------------------- update

// Update скачивает последний релиз и подменяет дистрибутив.
// Возвращает эффективную версию после установки.
func (m *Manager) Update(progress func(pct float64, stage string)) (string, error) {
	ctx := context.Background()
	tag, _, err := release.LatestZapret(ctx)
	if err != nil {
		return "", err
	}
	return m.UpdateTo(tag, progress)
}

// UpdateTo скачивает конкретный релиз (tag) и подменяет дистрибутив,
// сохраняя пользовательские файлы (списки *-user.txt, флаги настроек, ipset).
func (m *Manager) UpdateTo(tag string, progress func(pct float64, stage string)) (string, error) {
	if !m.Installed() {
		return "", errors.New("zapret не установлен")
	}
	stage := func(s string) {
		if progress != nil {
			progress(0, s)
		}
	}

	stage("Запрашиваю релиз " + tag + "…")
	ctx := context.Background()
	asset, err := release.ZapretReleaseAsset(ctx, tag)
	if err != nil {
		return "", err
	}

	tmpZip := filepath.Join(os.TempDir(), "zapret-gui-update.zip")
	stage("Скачиваю архив…")
	if err := release.Download(ctx, asset.URL, tmpZip, func(sent, total int64) {
		if progress != nil && total > 0 {
			progress(float64(sent)/float64(total)*0.8, "Скачиваю архив…")
		}
	}); err != nil {
		return "", err
	}
	defer os.Remove(tmpZip)

	stage("Распаковываю…")
	newDir := m.dir + ".new"
	oldDir := m.dir + ".old"
	os.RemoveAll(newDir)
	os.RemoveAll(oldDir)
	if err := release.Unzip(tmpZip, newDir); err != nil {
		return "", err
	}
	progress(0.9, "Переношу пользовательские файлы…")

	// останавливаем всё, что работает
	_ = m.StopProcess()
	wasService := false
	if st, _ := ServiceState(); st != "" {
		wasService = true
		strategy := ServiceStrategy()
		_ = ServiceRemove()
		defer func() { // восстановим службу с той же стратегией
			if wasService {
				_ = m.ServiceInstall(strategy)
			}
		}()
	}

	// сохраняем пользовательское
	preserve := []string{
		filepath.Join("lists", "list-general-user.txt"),
		filepath.Join("lists", "list-exclude-user.txt"),
		filepath.Join("lists", "ipset-exclude-user.txt"),
		filepath.Join("lists", "ipset-all.txt"),
		filepath.Join("lists", "ipset-all.txt.backup"),
		filepath.Join("utils", "game_filter.enabled"),
		filepath.Join("utils", "check_updates.enabled"),
	}
	for _, rel := range preserve {
		src := filepath.Join(m.dir, filepath.FromSlash(rel))
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(newDir, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := copyFile(src, dst); err != nil {
				return "", err
			}
		}
	}

	progress(0.97, "Заменяю каталог…")
	if err := os.Rename(m.dir, oldDir); err != nil {
		return "", err
	}
	if err := os.Rename(newDir, m.dir); err != nil {
		os.Rename(oldDir, m.dir) // откат
		return "", err
	}
	os.RemoveAll(oldDir)
	progress(1, "Готово")
	if m.onLog != nil {
		m.onLog("[zapret] установлена версия " + tag + " (" + release.ReadVersionFile(m.dir) + ")")
	}
	return firstNonEmptyLocal(m.Version(), tag), nil
}

func firstNonEmptyLocal(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
