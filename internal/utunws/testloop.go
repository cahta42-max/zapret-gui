//go:build darwin

package utunws

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"zapret_gui/internal/macutil"
)

// TestLoop — привилегированный перебор стратегий с ОДНИМ запросом пароля.
//
// Проблема: смена стратегии = запись selected-strategy + kickstart демона
// (root). 22 стратегии × osascript-пароль — пытка. Решение: root-скрипт
// (запущен одним osascript) крутит цикл сам, а с GUI общается через
// маркер-файлы в общем каталоге:
//
//	list.txt   — id стратегий по порядку (пишет GUI)
//	current    — «тестируйте эту» (пишет root, читает GUI)
//	done       — «тест URL завершён» (пишет GUI, читает root)
//	status     — заметки об ошибках root-стороны (FAIL <id> <причина>)
//	abort      — «прекратить» (пишет GUI)
//	finished   — root-скрипт завершился (в т.ч. восстановил состояние)
type TestLoop struct {
	dir       string
	mu        sync.Mutex
	adminDone chan error
	adminErr  error
	lastSeen  string
	failed    map[string]string
	closed    bool
}

// StartTestLoop готовит маркер-каталог и запускает root-скрипт.
// ids — стратегии по порядку; restoreID — что вернуть в selected-strategy
// в конце; wasRunning — был ли движок запущен (если нет — в конце stop.sh).
func (m *Manager) StartTestLoop(ids []string, restoreID string, wasRunning bool) (*TestLoop, error) {
	if !m.Installed() {
		return nil, fmt.Errorf("движок не установлен")
	}
	dir, err := os.MkdirTemp("", "zapretgui-test-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o777); err != nil { // root создаёт тут файлы
		os.RemoveAll(dir)
		return nil, err
	}

	write := func(name, content string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o666)
	}
	if err := write("list.txt", strings.Join(ids, "\n")+"\n"); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := write("orig", restoreID+"\n"); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := write("wasrunning", map[bool]string{true: "1\n", false: "0\n"}[wasRunning]); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	// ВАЖНО: заранее создаём только status/finished (пустые). Файлы
	// current/done/abort — сигнальные: root-скрипт проверяет ИХ НАЛИЧИЕ
	// (-f), поэтому создавать их до начала нельзя — тест прошёлся бы вхолостую.
	for _, f := range []string{"status", "finished"} {
		if err := write(f, ""); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "loop.sh"), []byte(testLoopScript), 0o777); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	tl := &TestLoop{dir: dir, adminDone: make(chan error, 1), failed: map[string]string{}}
	go func() {
		shell := fmt.Sprintf("/bin/sh %s %s %s", shq(filepath.Join(dir, "loop.sh")), shq(dir), shq(DataRoot()))
		err := macutil.RunAdminSync("Zapret GUI: тест стратегий движка", shell)
		tl.mu.Lock()
		tl.adminErr = err
		tl.closed = true
		tl.mu.Unlock()
		tl.adminDone <- err
	}()
	return tl, nil
}

// Next блокирующе ждёт следующую стратегию от root-скрипта.
// Возвращает id и признак «продолжаем». Отмена юзером/ошибка — false.
func (tl *TestLoop) Next(timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		// пароль отменили / osascript умер
		tl.mu.Lock()
		closed, adminErr := tl.closed, tl.adminErr
		tl.mu.Unlock()
		if closed && adminErr != nil {
			return "", false
		}

		data, _ := os.ReadFile(filepath.Join(tl.dir, "current"))
		cur := strings.TrimSpace(string(data))
		doneGone := false
		if _, err := os.Stat(filepath.Join(tl.dir, "done")); os.IsNotExist(err) {
			doneGone = true
		}
		if cur != "" && cur != tl.lastSeen && doneGone {
			tl.lastSeen = cur
			return cur, true
		}
		// root-скрипт всё прошёл (finished) и нового не будет
		if fin, _ := os.ReadFile(filepath.Join(tl.dir, "finished")); len(strings.TrimSpace(string(fin))) > 0 {
			return "", false
		}
		if closed && cur == tl.lastSeen {
			return "", false
		}
		if time.Now().After(deadline) {
			tl.Abort()
			return "", false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// Done сигнализирует root-скрипту, что URL-тесты стратегии завершены.
func (tl *TestLoop) Done(id string) {
	_ = os.WriteFile(filepath.Join(tl.dir, "done"), []byte(id+"\n"), 0o666)
}

// FailWhy — причина сбоя root-стороны для стратегии (пусто — не сбоила).
func (tl *TestLoop) FailWhy(id string) string {
	tl.readStatus()
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return tl.failed[id]
}

// readStatus разбирает status-файл (FAIL <id> <причина>).
func (tl *TestLoop) readStatus() {
	data, _ := os.ReadFile(filepath.Join(tl.dir, "status"))
	tl.mu.Lock()
	defer tl.mu.Unlock()
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 3 && f[0] == "FAIL" {
			tl.failed[f[1]] = strings.Join(f[2:], " ")
		}
	}
}

// Abort просит root-скрипт завершиться досрочно.
func (tl *TestLoop) Abort() {
	_ = os.WriteFile(filepath.Join(tl.dir, "abort"), []byte("1\n"), 0o666)
}

// Wait дожидается завершения root-скрипта и прибирает каталог.
func (tl *TestLoop) Wait() error {
	err := <-tl.adminDone
	// скрипт мог не дописать finished (отмена пароля) — страховка
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tl.mu.Lock()
		closed := tl.closed
		tl.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	os.RemoveAll(tl.dir)
	return err
}

// testLoopScript — sh-скрипт, исполняемый от root. Аргументы: MARKER DIR
// (каталог маркеров) и DATA_ROOT. В конце восстанавливает стратегию и
// состояние движка.
const testLoopScript = `#!/bin/sh
MARKER="$1"
DATA_ROOT="$2"
PLIST=/Library/LaunchDaemons/io.github.flowseal.zapretmac.plist
DAEMON=system/io.github.flowseal.zapretmac
BASE='/Library/Application Support/ZapretMac'

ensure_daemon() {
  /bin/launchctl enable "$DAEMON" >/dev/null 2>&1 || true
  /bin/launchctl print "$DAEMON" >/dev/null 2>&1 || \
    /bin/launchctl bootstrap system "$PLIST" >/dev/null 2>&1 || true
}

wait_tun() { # $1=предел в 0.1с
  I=0
  while [ "$I" -lt "$1" ]; do
    if /sbin/ifconfig utun50 >/dev/null 2>&1; then return 0; fi
    sleep 0.1
    I=$((I + 1))
  done
  return 1
}

while IFS= read -r S; do
  [ -n "$S" ] || continue
  [ -f "$MARKER/abort" ] && break

  printf '%s\n' "$S" > "$DATA_ROOT/selected-strategy"
  ensure_daemon
  /bin/launchctl kickstart -k "$DAEMON" >/dev/null 2>&1

  # старый туннель умирает не мгновенно — сначала ждём его исчезновения
  I=0
  while /sbin/ifconfig utun50 >/dev/null 2>&1 && [ "$I" -lt 30 ]; do
    sleep 0.1
    I=$((I + 1))
  done
  if ! wait_tun 100; then
    echo "FAIL $S utun50-timeout" >> "$MARKER/status"
  fi
  # run.sh после появления туннеля ещё настраивает ifconfig и pf-правила —
  # даём ему осесть, иначе первый URL-тест гоняется мимо перехвата
  sleep 0.5

  printf '%s\n' "$S" > "$MARKER/current"

  I=0
  while [ ! -f "$MARKER/done" ]; do
    [ -f "$MARKER/abort" ] && break
    sleep 0.1
    I=$((I + 1))
    if [ "$I" -gt 900 ]; then
      echo "FAIL $S done-timeout" >> "$MARKER/status"
      break
    fi
  done
  rm -f "$MARKER/done"
done < "$MARKER/list.txt"

# восстановление исходного состояния
ORIG=$(cat "$MARKER/orig" 2>/dev/null || printf 'general')
[ -n "$ORIG" ] || ORIG=general
printf '%s\n' "$ORIG" > "$DATA_ROOT/selected-strategy"
if [ "$(cat "$MARKER/wasrunning" 2>/dev/null)" = "1" ]; then
  ensure_daemon
  /bin/launchctl kickstart -k "$DAEMON" >/dev/null 2>&1
  wait_tun 100 || true
else
  /bin/sh "$BASE/stop.sh" >/dev/null 2>&1 || true
fi
printf 'ok\n' > "$MARKER/finished"
`
