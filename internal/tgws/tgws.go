// Package tgws управляет tg-ws-proxy: скачивание, запуск в портативном
// режиме, чтение конфигурации (секрет/порт), ссылка tg://proxy, автозапуск.
// Платформно-зависимые части (путь бинаря, установка, процессы, версия)
// живут в tgws_windows.go / tgws_darwin.go.
package tgws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Manager struct {
	dir   string // tools\tg-ws-proxy
	cmd   *exec.Cmd
	done  chan struct{}
	onLog func(string)

	verMu   sync.Mutex
	verOnce bool
	version string // кэш версии
}

func New(toolsDir string, onLog func(string)) *Manager {
	return &Manager{dir: filepath.Join(toolsDir, "tg-ws-proxy"), onLog: onLog}
}

func (m *Manager) Dir() string { return m.dir }

// Installed — скачан ли бинарь (проверка платформенная).
func (m *Manager) Installed() bool {
	_, err := os.Stat(m.exePath())
	return err == nil
}

// Version возвращает версию бинаря (способ — платформенный), с кэшем.
func (m *Manager) Version() string {
	m.verMu.Lock()
	defer m.verMu.Unlock()
	if !m.verOnce {
		m.verOnce = true
		m.version = m.versionFromBinary()
	}
	return m.version
}

func (m *Manager) invalidateVersion() {
	m.verMu.Lock()
	m.verOnce, m.version = false, ""
	m.verMu.Unlock()
}

// Install скачивает последний релиз и создаёт конфиг по умолчанию.
// Возвращает тег версии.
func (m *Manager) Install(progress func(pct float64, stage string)) (string, error) {
	if progress != nil {
		progress(0, "Скачиваю TgWsProxy…")
	}
	tag, err := m.fetchAndInstall(context.Background(), progress)
	if err != nil {
		return "", err
	}
	if progress != nil {
		progress(1, "Готово")
	}
	if err := m.ensureConfig(); err != nil {
		return tag, err
	}
	m.invalidateVersion()
	return tag, nil
}

// Update — то же, что Install (дистрибутив заменяем целиком).
func (m *Manager) Update(progress func(pct float64, stage string)) (string, error) {
	if m.Running() {
		_ = m.Stop()
	}
	return m.Install(progress)
}

// ---------------------------------------------------------------- config

// configPath — конфиг портативного режима (TgWsProxy_data рядом с бинарём),
// с фолбэком на стандартный каталог платформы (установленная ранее версия).
func (m *Manager) configPath() string {
	p := filepath.Join(filepath.Dir(m.exePath()), "TgWsProxy_data", "config.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if legacy := m.legacyConfigPath(); legacy != "" {
		return legacy
	}
	return p
}

type Config struct {
	Host             string   `json:"host"`
	Port             int      `json:"port"`
	Secret           string   `json:"secret"`
	DcIP             []string `json:"dc_ip,omitempty"`
	Verbose          bool     `json:"verbose"`
	BufKb            int      `json:"buf_kb,omitempty"`
	PoolSize         int      `json:"pool_size,omitempty"`
	LogMaxMb         float64  `json:"log_max_mb,omitempty"`
	CheckUpdates     bool     `json:"check_updates"`
	Cfproxy          bool     `json:"cfproxy"`
	CfproxyUserDom   string   `json:"cfproxy_user_domain,omitempty"`
	CfproxyWorkerDom string   `json:"cfproxy_worker_domain,omitempty"`
	ForceTestDc      bool     `json:"force_test_dc"`
	Autostart        bool     `json:"autostart"` // Windows: автозапуск при входе
}

// ensureConfig создаёт конфиг портативного режима. Если у пользователя уже
// стоял tg-ws-proxy — наследуем его секрет и настройки, чтобы уже
// настроенный Telegram продолжил работать.
func (m *Manager) ensureConfig() error {
	path := m.configPath()
	if _, err := os.Stat(path); err == nil {
		return nil // уже есть (портативный или унаследованный)
	}
	if legacy := m.legacyConfigPath(); legacy != "" {
		if data, err := os.ReadFile(legacy); err == nil {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, data, 0o644)
		}
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	cfg := Config{
		Host:         "127.0.0.1",
		Port:         1443,
		Secret:       hex.EncodeToString(secret),
		CheckUpdates: true,
		Cfproxy:      true,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, data, 0o644)
}

// GetConfig читает конфиг tg-ws-proxy (или дефолт).
func (m *Manager) GetConfig() Config {
	cfg := Config{Host: "127.0.0.1", Port: 1443}
	data, err := os.ReadFile(m.configPath())
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.Port == 0 {
		cfg.Port = 1443
	}
	return cfg
}

// SetAutostart включает/выключает автозапуск при входе (через их же конфиг;
// ключ работает на Windows, на macOS автозапуск делает GUI).
func (m *Manager) SetAutostart(on bool) error {
	if !m.Installed() {
		return errors.New("tg-ws-proxy не установлен")
	}
	if err := m.ensureConfig(); err != nil {
		return err
	}
	path := m.configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	raw["autostart"] = on
	out, _ := json.MarshalIndent(raw, "", "  ")
	return os.WriteFile(path, out, 0o644)
}

// ---------------------------------------------------------------- run

// Start запускает TgWsProxy в портативном режиме.
func (m *Manager) Start() error {
	if !m.Installed() {
		return errors.New("tg-ws-proxy не установлен")
	}
	_ = m.ensureConfig() // идемпотентно: создаст/унаследует при первом запуске
	// забираем управление: чужие экземпляры (включая старые
	// автозапуски) держат порт 1443
	if !m.ourRunning() && m.anyProxyRunning() {
		if m.onLog != nil {
			m.onLog("[tg-ws] останавливаю уже запущенный экземпляр прокси…")
		}
		m.killProxyProcesses()
		time.Sleep(500 * time.Millisecond)
	}
	if m.Running() {
		return nil
	}
	exe := m.exePath()
	if err := os.Chmod(exe, 0o755); err != nil { // на всякий случай (cp/hdiutil могли потерять бит)
		return err
	}
	cmd := exec.Command(exe, "--portable")
	cmd.Dir = m.dir
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.done = make(chan struct{})
	go func() {
		cmd.Wait()
		close(m.done)
		if m.onLog != nil {
			m.onLog(fmt.Sprintf("[tg-ws] процесс завершён (код %d)", cmd.ProcessState.ExitCode()))
		}
	}()
	// ждём, пока прокси поднимет порт (иначе старт «удачен», а Telegram
	// не подключится). НЮАНС PyInstaller-onefile: запущенный нами процесс
	// — bootloader-РОДИТЕЛЬ, порт открывает ДОЧЕРНИЙ процесс после
	// распаковки во %TEMP% — два живых TgWsProxy.exe — НОРМА, не сироты
	// (23.08: попытка 2 подняла порт за ~4.6с, попытка 1 не уложилась
	// в прежние 6с — на холодном старте распаковка дольше).
	for i := 0; i < 50 && !m.PortOpen(); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	if !m.PortOpen() {
		_ = m.Stop()
		return fmt.Errorf("порт %d не поднялся — возможно, занят другим приложением", m.GetConfig().Port)
	}
	if m.onLog != nil {
		m.onLog("[tg-ws] TgWsProxy запущен (портативный режим)")
	}
	return nil
}

func (m *Manager) ourRunning() bool {
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

// Stop останавливает наш процесс и чужие экземпляры TgWsProxy.
func (m *Manager) Stop() error {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		<-m.done
		m.cmd = nil
	}
	m.killProxyProcesses()
	return nil
}

// Running — работает ли прокси (наш процесс или любой TgWsProxy).
func (m *Manager) Running() bool {
	return m.ourRunning() || m.anyProxyRunning()
}

// PortOpen проверяет, слушает ли прокси локальный порт.
func (m *Manager) PortOpen() bool {
	cfg := m.GetConfig()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 700*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Link возвращает tg://proxy-ссылку для подключения Telegram.
func (m *Manager) Link() string {
	cfg := m.GetConfig()
	if cfg.Secret == "" {
		return ""
	}
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=%s", cfg.Host, cfg.Port, cfg.Secret)
}
