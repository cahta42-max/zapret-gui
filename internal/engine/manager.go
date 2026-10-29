//go:build darwin

package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"zapret_gui/internal/macutil"
	"zapret_gui/internal/release"
)

// DefaultPort — локальный порт HTTP-прокси spoofdpi.
const DefaultPort = 18080

// Manager управляет движком spoofdpi в каталоге tools/spoofdpi.
type Manager struct {
	dir   string // tools/spoofdpi
	port  int
	cmd   *exec.Cmd
	done  chan struct{}
	onLog func(string)

	rootProc bool // текущий процесс запущен через osascript (не наш ребёнок)
}

func New(toolsDir string, onLog func(string)) *Manager {
	return &Manager{dir: filepath.Join(toolsDir, "spoofdpi"), port: DefaultPort, onLog: onLog}
}

func (m *Manager) Dir() string { return m.dir }
func (m *Manager) Port() int   { return m.port }

func (m *Manager) BinPath() string { return filepath.Join(m.dir, "spoofdpi") }

// Installed — есть ли бинарь движка.
func (m *Manager) Installed() bool {
	st, err := os.Stat(m.BinPath())
	return err == nil && !st.IsDir()
}

// Version — spoofdpi --version (кэш не храним: запуск быстрый и редкий).
func (m *Manager) Version() string {
	if !m.Installed() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, m.BinPath(), "--version").Output()
	if err != nil {
		return ""
	}
	return strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
}

// Install скачивает последний релиз spoofdpi под текущую архитектуру.
// Возвращает тег версии.
func (m *Manager) Install(progress func(pct float64, stage string)) (string, error) {
	ctx := context.Background()
	tag, asset, err := release.LatestSpoofDPI(ctx, "darwin", arch())
	if err != nil {
		return "", err
	}
	if progress != nil {
		progress(0.1, "Скачиваю spoofdpi…")
	}
	tmpTgz := filepath.Join(os.TempDir(), "zapretgui-spoofdpi.tar.gz")
	if err := release.Download(ctx, asset.URL, tmpTgz, func(sent, total int64) {
		if progress != nil && total > 0 {
			progress(0.1+float64(sent)/float64(total)*0.7, "Скачиваю spoofdpi…")
		}
	}); err != nil {
		return "", err
	}
	defer os.Remove(tmpTgz)
	if progress != nil {
		progress(0.85, "Распаковываю…")
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", err
	}
	if err := release.Untar(tmpTgz, m.dir); err != nil {
		return "", err
	}
	if err := os.Chmod(m.BinPath(), 0o755); err != nil {
		return "", err
	}
	if progress != nil {
		progress(1, "Готово")
	}
	return tag, nil
}

// Update — переустановка на свежий релиз.
func (m *Manager) Update(progress func(pct float64, stage string)) (string, error) {
	if m.Running() {
		_ = m.Stop()
	}
	os.RemoveAll(m.dir)
	return m.Install(progress)
}

// UpdateToTag устанавливает конкретную версию релиза.
func (m *Manager) UpdateToTag(tag string, progress func(pct float64, stage string)) error {
	if m.Running() {
		_ = m.Stop()
	}
	os.RemoveAll(m.dir)
	ctx := context.Background()
	asset, err := release.SpoofDPIReleaseAsset(ctx, tag, "darwin", runtime.GOARCH)
	if err != nil {
		return err
	}
	tmpTgz := filepath.Join(os.TempDir(), "zapretgui-spoofdpi.tar.gz")
	if err := release.Download(ctx, asset.URL, tmpTgz, func(sent, total int64) {
		if progress != nil && total > 0 {
			progress(float64(sent)/float64(total), "Скачиваю spoofdpi…")
		}
	}); err != nil {
		return err
	}
	defer os.Remove(tmpTgz)
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	if err := release.Untar(tmpTgz, m.dir); err != nil {
		return err
	}
	return os.Chmod(m.BinPath(), 0o755)
}

func arch() string { return runtime.GOARCH }

// ---------------------------------------------------------------- запуск

// Start запускает движок с профилем. Для профилей с raw-сокетами
// (NeedsRoot) при отсутствии прав — osascript-запрос администратора.
func (m *Manager) Start(profile Profile) error {
	if !m.Installed() {
		return errors.New("движок не установлен")
	}
	if err := m.Stop(); err != nil {
		return err
	}
	args := append(BaseArgs(m.port), profile.Args...)
	bin := m.BinPath()

	if profile.NeedsRoot && !macutil.IsRoot() {
		if err := macutil.RunAsRootDetached(bin, args...); err != nil {
			return fmt.Errorf("запуск с правами администратора: %v", err)
		}
		m.rootProc = true
	} else {
		cmd := exec.Command(bin, args...)
		cmd.Dir = m.dir
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
		}()
		m.rootProc = false
	}

	// ждём, пока поднимется порт прокси
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if m.PortOpen() {
			if m.onLog != nil {
				m.onLog(fmt.Sprintf("[движок] spoofdpi запущен (профиль «%s», порт %d)", profile.Title, m.port))
			}
			return nil
		}
		// наш дочерний процесс мог упасть сразу
		if !m.rootProc && m.cmd != nil {
			select {
			case <-m.done:
				return fmt.Errorf("spoofdpi завершился сразу — профиль «%s» не поддерживается системой", profile.Title)
			default:
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = m.Stop()
	return fmt.Errorf("порт %d не поднялся за 6 с", m.port)
}

func (m *Manager) pumpLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		if m.onLog != nil && strings.TrimSpace(sc.Text()) != "" {
			m.onLog("[движок] " + sc.Text())
		}
	}
}

// PortOpen — слушает ли движок свой порт.
func (m *Manager) PortOpen() bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(m.port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ourRunning — жив ли наш дочерний процесс.
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

// Running — работает ли движок (наш процесс или запущенный от root).
func (m *Manager) Running() bool {
	return m.ourRunning() || m.rootRunning()
}

func (m *Manager) rootRunning() bool {
	out, err := macutil.Output("pgrep", "-f", m.BinPath())
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// Stop останавливает движок (наш процесс и root-экземпляр).
func (m *Manager) Stop() error {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		<-m.done
		m.cmd = nil
	}
	// root-экземпляр (и осиротевшие): pkill по точному пути бинаря
	if m.rootRunning() {
		macutil.Silent("pkill", "-f", m.BinPath())
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && m.rootRunning() {
			time.Sleep(150 * time.Millisecond)
		}
	}
	m.rootProc = false
	return nil
}
